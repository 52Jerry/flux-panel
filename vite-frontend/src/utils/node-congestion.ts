/** Optional telemetry. Missing/unsupported samples must never become healthy zeroes. */
export interface NodeCongestion {
  observed_at: number | null;
  active_transfers: number | null;
  bytes_forwarded_total: number | null;
  write_stalls_total: number | null;
  write_timeouts_total: number | null;
  active_stalled_writes: number | null;
  max_write_block_ms: number | null;
  tcp_sampling_supported: boolean | null;
  tcp_sampling_limited: boolean | null;
  tcp_tracked_connections: number | null;
  tcp_sampled_connections: number | null;
  tcp_unavailable_connections: number | null;
  tcp_sample_errors: number | null;
  tcp_rtt_ms_avg: number | null;
  tcp_min_rtt_ms: number | null;
  tcp_retransmissions: number | null;
  tcp_notsent_bytes: number | null;
  tcp_delivery_rate_bps: number | null;
  tcp_bbr_connections: number | null;
  tcp_cubic_connections: number | null;
}

const count = (value: unknown): number | null =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null;
const measurement = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;

export function parseNodeCongestion(value: unknown): NodeCongestion | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const data = value as Record<string, unknown>;
  const supported = typeof data.tcp_sampling_supported === 'boolean' ? data.tcp_sampling_supported : null;
  const sampled = count(data.tcp_sampled_connections);
  // User-space transports and failed/empty sampling do not provide kernel TCP metrics.
  const hasSamples = supported === true && sampled !== null && sampled > 0;
  return {
    observed_at: count(data.observed_at),
    active_transfers: count(data.active_transfers),
    bytes_forwarded_total: count(data.bytes_forwarded_total),
    write_stalls_total: count(data.write_stalls_total),
    write_timeouts_total: count(data.write_timeouts_total),
    active_stalled_writes: count(data.active_stalled_writes),
    max_write_block_ms: measurement(data.max_write_block_ms),
    tcp_sampling_supported: supported,
    tcp_sampling_limited: typeof data.tcp_sampling_limited === 'boolean' ? data.tcp_sampling_limited : null,
    tcp_tracked_connections: count(data.tcp_tracked_connections),
    tcp_sampled_connections: sampled,
    tcp_unavailable_connections: count(data.tcp_unavailable_connections),
    tcp_sample_errors: count(data.tcp_sample_errors),
    tcp_rtt_ms_avg: hasSamples ? measurement(data.tcp_rtt_ms_avg) : null,
    tcp_min_rtt_ms: hasSamples ? measurement(data.tcp_min_rtt_ms) : null,
    tcp_retransmissions: hasSamples ? count(data.tcp_retransmissions) : null,
    tcp_notsent_bytes: hasSamples ? count(data.tcp_notsent_bytes) : null,
    tcp_delivery_rate_bps: hasSamples ? measurement(data.tcp_delivery_rate_bps) : null,
    tcp_bbr_connections: hasSamples ? count(data.tcp_bbr_connections) : null,
    tcp_cubic_connections: hasSamples ? count(data.tcp_cubic_connections) : null,
  };
}

export function formatCongestionValue(value: number | null | undefined, unit = ''): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '暂无数据';
  return `${value.toLocaleString('zh-CN', { maximumFractionDigits: 2 })}${unit}`;
}

export function formatCongestionBytes(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '暂无数据';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const index = value < 1 ? 0 : Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return formatCongestionValue(value / 1024 ** index, ` ${units[index]}`);
}

export function formatCongestionTime(value: number | null | undefined): string {
  if (value == null || value <= 0 || !Number.isFinite(new Date(value).getTime())) return '暂无数据';
  return new Date(value).toLocaleTimeString('zh-CN', { hour12: false });
}
