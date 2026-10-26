export interface FlowSnapshot {
  id?: number;
  flow: number;
  totalFlow: number;
  time: string;
  createdTime?: number;
}

const HOUR = 60 * 60 * 1000;
const hourStart = (value: number) => {
  const date = new Date(value);
  date.setMinutes(0, 0, 0);
  return date.getTime();
};
const validBytes = (value: number) => Number.isFinite(value) && value >= 0;

// Persisted HH:00 snapshots measure the hour ending at HH:00. Render them
// under that interval's start, then append the current, incomplete hour.
export function dashboardFlowData(records: FlowSnapshot[], total: number, observedAt: number) {
  const current = hourStart(observedAt);
  const completed = new Map<number, FlowSnapshot>();
  let latest: { end: number; record: FlowSnapshot } | undefined;
  for (const record of records) {
    // The API pads missing hours with rows lacking an id/timestamp. Never let
    // these placeholders replace a real measurement from another date.
    if (!record.id || !validBytes(record.flow) || !validBytes(record.totalFlow)) continue;
    let end: number;
    if (Number.isFinite(record.createdTime) && record.createdTime! > 0) {
      end = hourStart(record.createdTime!);
    } else {
      const match = /^(\d{2}):00$/.exec(record.time);
      if (!match || Number(match[1]) > 23) continue;
      const date = new Date(current);
      date.setHours(Number(match[1]));
      if (date.getTime() > current) date.setDate(date.getDate() - 1);
      end = date.getTime();
    }
    if (end > current || end < current - 24 * HOUR) continue;
    const key = end - HOUR;
    if ((completed.get(key)?.id ?? -1) < record.id) completed.set(key, record);
    if (!latest || end > latest.end || (end === latest.end && record.id > latest.record.id!)) {
      latest = { end, record };
    }
  }
  return Array.from({ length: 24 }, (_, index) => {
    const timestamp = current - (23 - index) * HOUR;
    const date = new Date(timestamp);
    const time = `${date.getHours().toString().padStart(2, '0')}:00`;
    const isCurrent = timestamp === current;
    let flow: number | null = completed.get(timestamp)?.flow ?? null;
    if (isCurrent && latest?.end === current && validBytes(total)) {
      // A quota reset can lower the cumulative counter; do not graph a negative.
      flow = total >= latest.record.totalFlow ? total - latest.record.totalFlow : total;
    }
    return { time, timestamp, flow, isCurrent };
  });
}
