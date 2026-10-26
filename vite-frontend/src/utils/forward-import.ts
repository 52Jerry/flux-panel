export interface ForwardImportResult {
  line: string;
  success: boolean;
  message: string;
  forwardName?: string;
}

export interface ForwardImportProgress {
  // Append-only during an import. Render only the prefix covered by total.
  results: readonly ForwardImportResult[];
  total: number;
  success: number;
}

interface ForwardImportOptions {
  text: string;
  tunnelId: number;
  createForward: (data: {
    name: string; tunnelId: number; inPort: number | null;
    remoteAddr: string; strategy: string;
  }) => Promise<{ code: number; msg?: string }>;
  onProgress: (progress: ForwardImportProgress) => void;
  shouldStop: () => boolean;
  yieldToBrowser?: () => Promise<void>;
}

export const IMPORT_PAGE_SIZE = 50;

export function importResultPage(progress: ForwardImportProgress, page: number) {
  const end = Math.max(0, progress.total - (page - 1) * IMPORT_PAGE_SIZE);
  return progress.results.slice(Math.max(0, end - IMPORT_PAGE_SIZE), end).reverse();
}

export async function runForwardImport(options: ForwardImportOptions) {
  const { text, tunnelId, createForward, onProgress, shouldStop } = options;
  const yieldToBrowser = options.yieldToBrowser ?? (() => new Promise<void>(resolve => setTimeout(resolve, 0)));
  const results: ForwardImportResult[] = [];
  let success = 0;
  let lastPublished = 0;
  const publish = () => {
    if (!shouldStop()) onProgress({ results, total: results.length, success });
    lastPublished = Date.now();
  };

  const processLine = async (line: string): Promise<ForwardImportResult> => {
    const fail = (message: string) => ({ line, success: false, message });
    const parts = line.split('|');
    if (parts.length < 2) return fail('格式错误：需要至少包含目标地址和转发名称');
    const [remoteAddr, name, inPort] = parts;
    if (!remoteAddr.trim() || !name.trim()) return fail('目标地址和转发名称不能为空');
    if (!remoteAddr.trim().split(',').every(addr => /^[^:]+:\d+$/.test(addr.trim()))) {
      return fail('目标地址格式错误，应为 地址:端口 格式，多个地址用逗号分隔');
    }
    let portNumber: number | null = null;
    if (inPort?.trim()) {
      const port = Number(inPort.trim());
      if (!Number.isInteger(port) || port < 1 || port > 65535) {
        return fail('入口端口格式错误，应为1-65535之间的数字');
      }
      portNumber = port;
    }
    try {
      const response = await createForward({
        name: name.trim(), tunnelId, inPort: portNumber,
        remoteAddr: remoteAddr.trim(), strategy: 'fifo',
      });
      return response.code === 0
        ? { line, success: true, message: '创建成功', forwardName: name.trim() }
        : fail(response.msg || '创建失败');
    } catch {
      return fail('网络错误，创建失败');
    }
  };

  // Scan lines incrementally rather than allocating another complete split array.
  const linePattern = /[^\r\n]+/g;
  let match: RegExpExecArray | null;
  while ((match = linePattern.exec(text)) !== null) {
    if (shouldStop()) return false;
    const line = match[0].trim();
    if (!line) continue;
    const result = await processLine(line);
    if (shouldStop()) return false;
    results.push(result);
    if (result.success) success++;
    if (results.length % IMPORT_PAGE_SIZE === 0 || Date.now() - lastPublished >= 200) publish();
    // Invalid rows have no network await; let input, paint, and cancellation run.
    if (results.length % IMPORT_PAGE_SIZE === 0) await yieldToBrowser();
  }
  if (shouldStop()) return false;
  publish();
  return true;
}
