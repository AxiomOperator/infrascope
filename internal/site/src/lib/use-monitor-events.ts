import { useEffect, useMemo, useState } from "react"
import { pb } from "@/lib/api"
import type { MonitorEventRecord } from "@/types"

const DAY_MS = 86_400_000

export interface MonitorDayUptime {
	/** Local midnight of the day in milliseconds. */
	start: number
	/** Uptime percentage; null when there is no up or down time that day. */
	uptime: number | null
	upMs: number
	downMs: number
	/** Number of down events that overlap the day. */
	incidents: number
}

/**
 * Fetches a monitor's status events of the last `days` days, oldest first.
 * Refetches when `refreshKey` changes (e.g. the monitor's statusChanged).
 */
export function useMonitorEvents({
	monitorId,
	days = 30,
	enabled = true,
	refreshKey,
}: {
	monitorId: string
	days?: number
	enabled?: boolean
	refreshKey?: unknown
}) {
	const [events, setEvents] = useState<MonitorEventRecord[]>([])
	const [isLoading, setIsLoading] = useState(true)

	useEffect(() => {
		if (!enabled) {
			return
		}
		let cancelled = false
		setIsLoading(true)
		const since = startOfDay(Date.now()) - (days - 1) * DAY_MS
		pb.collection<MonitorEventRecord>("monitor_events")
			.getFullList({
				filter: pb.filter("monitor = {:id} && (end = 0 || end >= {:since})", { id: monitorId, since }),
				fields: "id,monitor,status,start,end,error,statusCode",
				sort: "start",
			})
			.then((records) => {
				if (!cancelled) setEvents(records)
			})
			.catch((error) => {
				if (!cancelled) console.error("Failed to fetch monitor events:", error)
			})
			.finally(() => {
				if (!cancelled) setIsLoading(false)
			})
		return () => {
			cancelled = true
		}
	}, [monitorId, days, enabled, refreshKey])

	return { events, isLoading }
}

function startOfDay(time: number) {
	const date = new Date(time)
	date.setHours(0, 0, 0, 0)
	return date.getTime()
}

/**
 * Per-day uptime of the last `days` local days, oldest first. Maintenance,
 * paused and unknown time is excluded from the denominator.
 */
export function getDailyUptime(events: MonitorEventRecord[], days = 30, now = Date.now()): MonitorDayUptime[] {
	const dayStarts: number[] = []
	let dayStart = startOfDay(now)
	for (let i = 0; i < days; i++) {
		dayStarts.unshift(dayStart)
		// step back via the calendar so DST changes don't shift days
		const date = new Date(dayStart)
		date.setDate(date.getDate() - 1)
		dayStart = date.getTime()
	}
	const result: MonitorDayUptime[] = dayStarts.map((start) => ({
		start,
		uptime: null,
		upMs: 0,
		downMs: 0,
		incidents: 0,
	}))

	for (const event of events) {
		if (event.status !== "up" && event.status !== "down") continue
		const eventEnd = event.end || now
		for (let i = 0; i < result.length; i++) {
			const day = result[i]
			const dayEnd = i + 1 < result.length ? result[i + 1].start : now
			const overlap = Math.min(eventEnd, dayEnd) - Math.max(event.start, day.start)
			if (overlap <= 0) continue
			if (event.status === "up") {
				day.upMs += overlap
			} else {
				day.downMs += overlap
				day.incidents++
			}
		}
	}

	for (const day of result) {
		const total = day.upMs + day.downMs
		if (total > 0) day.uptime = (day.upMs / total) * 100
	}
	return result
}

/** Down events, newest first. */
export function useMonitorIncidents(events: MonitorEventRecord[]) {
	return useMemo(() => events.filter((event) => event.status === "down").reverse(), [events])
}
