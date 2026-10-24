export interface NodeConfig {
  id: number;
  name?: string | null;
  ip?: string | null;
  serverIp?: string | null;
  portSta?: number | null;
  portEnd?: number | null;
  port?: string | null;
  interfaceName?: string | null;
  tcpListenAddr?: string | null;
  udpListenAddr?: string | null;
  http?: number | null;
  tls?: number | null;
  socks?: number | null;
}

export interface NodeForm {
  id: number | null;
  schema: 'legacy' | 'modern';
  name: string;
  ipString: string;
  serverIp: string;
  portSta: number;
  portEnd: number;
  port: string;
  interfaceName: string;
  tcpListenAddr: string;
  udpListenAddr: string;
  http: number;
  tls: number;
  socks: number;
}

export const isModernNode = (node: NodeConfig): boolean =>
  Object.prototype.hasOwnProperty.call(node, 'port');

export function emptyNodeForm(schema: NodeForm['schema'] = 'legacy'): NodeForm {
  return {
    id: null, schema, name: '', ipString: '', serverIp: '',
    portSta: 1000, portEnd: 65535, port: '1000-65535',
    interfaceName: '', tcpListenAddr: '[::]', udpListenAddr: '[::]',
    http: 0, tls: 0, socks: 0,
  };
}

export function nodeToForm(node: NodeConfig): NodeForm {
  return {
    ...emptyNodeForm(isModernNode(node) ? 'modern' : 'legacy'),
    id: node.id,
    name: node.name ?? '',
    ipString: (node.ip ?? '').split(',').map(ip => ip.trim()).join('\n'),
    serverIp: node.serverIp ?? '',
    portSta: node.portSta ?? 1000,
    portEnd: node.portEnd ?? 65535,
    // Never replace an existing multi-range configuration with a default range.
    port: node.port ?? '',
    interfaceName: node.interfaceName ?? '',
    tcpListenAddr: node.tcpListenAddr ?? '[::]',
    udpListenAddr: node.udpListenAddr ?? '[::]',
    http: node.http ?? 0,
    tls: node.tls ?? 0,
    socks: node.socks ?? 0,
  };
}

export const normalizePorts = (port: string): string => port.replace(/\s/g, '');

export function validPorts(port: string): boolean {
  const parts = normalizePorts(port).split(',');
  return parts.every(part => {
    if (!/^\d{1,5}(-\d{1,5})?$/.test(part)) return false;
    const [start, end = start] = part.split('-').map(Number);
    return start >= 1 && end <= 65535 && start <= end;
  });
}

export function nodePayload(form: NodeForm) {
  const common = {
    ...(form.id !== null ? { id: form.id } : {}),
    name: form.name.trim(), serverIp: form.serverIp.trim(),
    http: form.http, tls: form.tls, socks: form.socks,
  };
  if (form.schema === 'modern') {
    return {
      ...common, port: normalizePorts(form.port),
      interfaceName: form.interfaceName.trim(),
      tcpListenAddr: form.tcpListenAddr.trim(),
      udpListenAddr: form.udpListenAddr.trim(),
    };
  }
  return {
    ...common,
    ip: form.ipString.split('\n').map(ip => ip.trim()).filter(Boolean).join(','),
    portSta: form.portSta, portEnd: form.portEnd,
  };
}

export function nodePortLabel(node: NodeConfig): string {
  if (isModernNode(node)) return node.port || '-';
  return node.portSta != null && node.portEnd != null
    ? `${node.portSta}-${node.portEnd}` : '-';
}
