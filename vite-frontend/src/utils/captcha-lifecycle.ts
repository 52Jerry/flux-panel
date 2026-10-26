interface CaptchaRequest {
  url: string;
  method?: string;
  headers?: Record<string, string>;
  data?: unknown;
}

// The vendored renderer remains responsible for CAPTCHA types and DOM. Keep
// request ownership and cancellation here so they can be tested independently.
export class CaptchaRequests {
  private cancelActive: (() => void) | null = null;

  constructor(private readonly timeoutMs = 15000) {}

  cancel() {
    this.cancelActive?.();
  }

  send(request: CaptchaRequest): Promise<any> {
    this.cancel();
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      let settled = false;
      const finish = (error?: Error, value?: unknown) => {
        if (settled) return;
        settled = true;
        xhr.onreadystatechange = xhr.onerror = xhr.ontimeout = xhr.onabort = null;
        if (this.cancelActive === cancel) this.cancelActive = null;
        if (error) reject(error);
        else resolve(value);
      };
      const cancel = () => {
        finish(new DOMException('验证码请求已取消', 'AbortError'));
        xhr.abort();
      };
      this.cancelActive = cancel;
      try {
        xhr.open(request.method || 'POST', request.url);
        xhr.timeout = this.timeoutMs;
        for (const [key, value] of Object.entries(request.headers || {})) xhr.setRequestHeader(key, value);
        xhr.onreadystatechange = () => {
          if (xhr.readyState !== XMLHttpRequest.DONE) return;
          if (xhr.status < 200 || xhr.status >= 300) {
            finish(new Error(`验证码请求失败 (${xhr.status})，请重试`));
            return;
          }
          try {
            finish(undefined, JSON.parse(xhr.responseText));
          } catch {
            finish(new Error('验证码响应格式错误，请重试'));
          }
        };
        xhr.onerror = () => finish(new Error('验证码网络请求失败，请重试'));
        xhr.ontimeout = () => finish(new Error('验证码请求超时，请重试'));
        xhr.onabort = () => finish(new DOMException('验证码请求已取消', 'AbortError'));
        xhr.send(typeof request.data === 'string' ? request.data : JSON.stringify(request.data));
      } catch (error) {
        finish(error instanceof Error ? error : new Error('验证码请求失败，请重试'));
        xhr.abort();
      }
    });
  }
}

interface CaptchaRenderer {
  destroy: () => void;
  el: { css: (property: string, value: string) => void };
  showTips: (message: string, status: number, callback: () => void) => void;
}

interface ManagedCaptcha {
  init: () => unknown;
  destroyed: boolean;
  requestSequence: number;
  config: {
    requestHeaders?: Record<string, string>;
    validCaptchaUrl: string;
    doSendRequest: (request: CaptchaRequest) => Promise<any>;
    onRequestError: (error: Error) => void;
    validCaptcha: (id: string, data: unknown, captcha: CaptchaRenderer) => Promise<void>;
    validSuccess: (response: any, captcha: CaptchaRenderer, tac: ManagedCaptcha) => void;
    validFail: (response: any, captcha: CaptchaRenderer, tac: ManagedCaptcha) => void;
  };
  showLoading: () => void;
  createCaptcha: () => Promise<void>;
  destroyCaptcha: (callback?: () => void) => void;
  openCaptcha: () => void;
  reloadCaptcha: () => void;
  destroyWindow: () => void;
}

export function manageCaptcha<T extends { destroyWindow: () => void }>(
  instance: T, onError: (error: Error) => void, timeoutMs = 15000,
): T {
  const tac = instance as unknown as ManagedCaptcha;
  const requests = new CaptchaRequests(timeoutMs);
  const init = tac.init.bind(tac);
  const destroyWindow = tac.destroyWindow.bind(tac);
  let initializing = false;
  let ownedCaptcha: CaptchaRenderer | undefined;
  let openTimer: ReturnType<typeof setTimeout> | undefined;
  tac.destroyed = false;
  tac.requestSequence = 0;

  tac.init = () => {
    if (tac.destroyed) return;
    initializing = true;
    try {
      return init();
    } finally {
      initializing = false;
    }
  };

  const isCurrent = (sequence: number) => !tac.destroyed && sequence === tac.requestSequence;
  const clearTimer = () => {
    if (openTimer !== undefined) clearTimeout(openTimer);
    openTimer = undefined;
  };
  tac.config.doSendRequest = request => requests.send(request);
  tac.config.onRequestError = error => {
    if (tac.destroyed || error?.name === 'AbortError') return;
    tac.destroyWindow();
    onError(error);
  };
  tac.openCaptcha = () => {
    if (tac.destroyed) return;
    ownedCaptcha = window.currentCaptcha;
    const captcha = ownedCaptcha;
    const sequence = tac.requestSequence;
    clearTimer();
    openTimer = setTimeout(() => {
      openTimer = undefined;
      if (isCurrent(sequence)) captcha?.el.css('transform', 'translateX(0)');
    }, 10);
  };
  tac.destroyCaptcha = callback => {
    clearTimer();
    if (ownedCaptcha) {
      ownedCaptcha.destroy();
      if (window.currentCaptcha === ownedCaptcha) window.currentCaptcha = undefined;
      ownedCaptcha = undefined;
    }
    if (!tac.destroyed) callback?.();
  };
  tac.reloadCaptcha = () => {
    if (tac.destroyed) return;
    tac.requestSequence++;
    requests.cancel();
    tac.showLoading();
    tac.destroyCaptcha(() => { void tac.createCaptcha(); });
  };
  tac.destroyWindow = () => {
    // TAC.init starts by removing any old template through destroyWindow. That
    // internal template cleanup must not dispose the new request owner.
    if (initializing) {
      destroyWindow();
      return;
    }
    if (tac.destroyed) return;
    tac.destroyed = true;
    tac.requestSequence++;
    requests.cancel();
    tac.destroyCaptcha();
    destroyWindow();
  };
  // The bundled verification failure handler assumes absent i18n fields. Handle
  // errors here and guard the renderer's delayed success/failure tip callbacks.
  tac.config.validCaptcha = async (id, data, captcha) => {
    const sequence = tac.requestSequence;
    try {
      const response = await requests.send({
        url: tac.config.validCaptchaUrl, method: 'POST',
        headers: { ...tac.config.requestHeaders, 'Content-Type': 'application/json;charset=UTF-8' },
        data: { id, data },
      });
      if (!isCurrent(sequence)) return;
      const valid = response?.code === 200;
      captcha.showTips(valid ? '验证成功' : '验证失败，请重新尝试', valid ? 1 : 0, () => {
        if (!isCurrent(sequence)) return;
        if (valid) tac.config.validSuccess(response, captcha, tac);
        else tac.config.validFail(response, captcha, tac);
      });
    } catch (error) {
      if (isCurrent(sequence)) tac.config.onRequestError(error as Error);
    }
  };
  return instance;
}
