import { formatCongestionBytes, formatCongestionTime, formatCongestionValue } from '@/utils/node-congestion';
import type { NodeCongestion } from '@/utils/node-congestion';

export function NodeCongestionDetails({ data }: { data?: NodeCongestion | null }) {
  const primary = [
    ['活动转发', formatCongestionValue(data?.active_transfers)],
    ['当前停滞写入', formatCongestionValue(data?.active_stalled_writes)],
    ['TCP 平均往返', formatCongestionValue(data?.tcp_rtt_ms_avg, ' ms')],
    ['内核待发送', formatCongestionBytes(data?.tcp_notsent_bytes)],
    ['活动连接重传', formatCongestionValue(data?.tcp_retransmissions)],
  ];
  const secondary = [
    ['累计写入超时', formatCongestionValue(data?.write_timeouts_total)],
    ['累计写入停滞', formatCongestionValue(data?.write_stalls_total)],
    ['最长写入等待', formatCongestionValue(data?.max_write_block_ms, ' ms')],
    ['累计转交字节', formatCongestionBytes(data?.bytes_forwarded_total)],
    ['TCP 采样连接', `${formatCongestionValue(data?.tcp_sampled_connections)} / ${formatCongestionValue(data?.tcp_tracked_connections)}`],
    ['本次无法采样', formatCongestionValue(data?.tcp_unavailable_connections)],
    ['本次采样失败', formatCongestionValue(data?.tcp_sample_errors)],
    ['BBR 连接（样本）', formatCongestionValue(data?.tcp_bbr_connections)],
    ['CUBIC 连接（样本）', formatCongestionValue(data?.tcp_cubic_connections)],
    ['采样时间', formatCongestionTime(data?.observed_at)],
  ];

  return (
    <details className="mb-4 rounded border border-divider p-2 text-xs" data-testid="node-congestion">
      <summary className="cursor-pointer text-default-600">转发链路</summary>
      <dl className="mt-2 space-y-1.5">
        {primary.map(([label, value]) => (
          <div key={label} className="flex justify-between gap-2">
            <dt className="text-default-600">{label}</dt>
            <dd className="text-right font-mono">{value}</dd>
          </div>
        ))}
      </dl>
      <p className="mt-2 text-default-500 leading-relaxed">
        {!data ? '暂无数据：等待节点上报，旧版本节点可能不支持。' :
          data.tcp_sampling_supported === false ? '当前节点不支持内核 TCP 采样；仍可查看转发写入状态。' :
          !data.tcp_sampled_connections ? '暂无内核 TCP 样本；空闲或用户态连接（如 WireGuard）不提供这些指标。' :
          'TCP 指标为活动连接抽样。重传数是样本连接各自生命周期的累计值，并非丢包率；内核待发送是样本连接尚未发出的排队字节。'}
      </p>
      {data?.tcp_sampling_limited && <p className="mt-1 text-default-500">已达采样数量限制，数据仅覆盖部分连接。</p>}
      <dl className="mt-2 border-t border-divider pt-2 space-y-1.5">
        {secondary.map(([label, value]) => (
          <div key={label} className="flex justify-between gap-2">
            <dt className="text-default-600">{label}</dt>
            <dd className="text-right font-mono">{value}</dd>
          </div>
        ))}
      </dl>
      <p className="mt-2 text-default-500 leading-relaxed">算法计数仅反映采样连接，不表示整条链路已启用 BBR。</p>
      <p className="mt-2 text-default-500 leading-relaxed">累计转交字节表示写入下游连接成功，不代表对端已收到。</p>
    </details>
  );
}
