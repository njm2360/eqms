import type { EqEvent, EventsPage, WaveformRange } from "./types";

export const EVENTS_PAGE_SIZE = 50;

async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(url, { signal });
  if (!res.ok) throw new Error(`${url}: ${res.status}`);
  return res.json();
}

export interface EventsCursor {
  before: number; // startedAt (ms epoch)
  beforeId: number;
}

// 次ページは応答の nextBefore / nextBeforeId をそのまま渡す。
export const fetchEvents = (limit = EVENTS_PAGE_SIZE, cursor?: EventsCursor) =>
  getJSON<EventsPage>(
    `/api/events?limit=${limit}` + (cursor ? `&before=${cursor.before}&beforeId=${cursor.beforeId}` : ""),
  );

export const nextCursor = (page: EventsPage): EventsCursor | null =>
  page.nextBefore !== null && page.nextBeforeId !== null
    ? { before: page.nextBefore, beforeId: page.nextBeforeId }
    : null;

export const fetchEvent = (id: number) => getJSON<EqEvent>(`/api/events/${id}`);

export const fetchWaveform = (from: number, to: number, points: number, signal?: AbortSignal) =>
  getJSON<WaveformRange>(`/api/waveform?from=${Math.floor(from)}&to=${Math.ceil(to)}&points=${points}`, signal);
