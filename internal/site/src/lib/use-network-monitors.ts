import { chartTimeData } from "@/lib/utils"
import { clearFailedResponse, getMonitorStats, withMonitorGaps } from "@/lib/network-monitor-utils"
import { getMonitorLocations } from "@/lib/monitor-locations"
import type {
	ChartTimes,
	MonitorStats,
	NetworkMonitorRecord,
	NetworkMonitorStatsRecord,
	RawMonitorStatsRecord,
} from "@/types"
import { useEffect, useMemo, useRef, useState } from "react"
import { appendData } from "@/components/routes/system/chart-data"
import { pb, getPbTimestamp } from "@/lib/api"
import { toast } from "@/components/ui/use-toast"
import type { RecordListOptions, RecordSubscription } from "pocketbase"

const cache = new Map<string, NetworkMonitorStatsRecord[]>()

/** Stats of one location of a monitor ("" for the hub), or of all locations when undefined. */
export type MonitorStatsScope = { location?: string; byLocation?: boolean }

/** Key of a monitor's stats in the cache: the monitor id, with the location or "*" for per-location series. */
function statsKey(monitorId: string, scope: MonitorStatsScope = {}) {
	if (scope.location !== undefined) return `${monitorId}@${scope.location}`
	return scope.byLocation ? `${monitorId}@*` : monitorId
}

/** Series key of a location's stats of a monitor in per-location chart records. */
export function locationSeriesKey(monitorId: string, system: string) {
	return `${monitorId}@${system}`
}

function getCacheValue(monitorId: string, chartTime: ChartTimes | "rt") {
	return cache.get(`${monitorId}:${chartTime}`) || []
}

function appendCacheValue(
	monitorId: string,
	chartTime: ChartTimes | "rt",
	newStats: NetworkMonitorStatsRecord[],
	maxPoints = 100
) {
	const cache_key = `${monitorId}:${chartTime}`
	const existingStats = getCacheValue(monitorId, chartTime)
	if (existingStats) {
		const { expectedInterval } = chartTimeData[chartTime]
		const updatedStats = appendData(existingStats, newStats, expectedInterval, maxPoints)
		cache.set(cache_key, updatedStats)
		return updatedStats
	} else {
		cache.set(cache_key, newStats)
		return newStats
	}
}

/**
 * Merge an array of per-monitor raw records into the map-keyed format expected by chart components.
 * With byLocation, each location's records get their own series (see locationSeriesKey).
 */
export function mergeMonitorStats(
	rawRecords: RawMonitorStatsRecord[],
	byLocation = false
): NetworkMonitorStatsRecord[] {
	const byTimestamp = new Map<number, Record<string, MonitorStats>>()
	for (const rec of rawRecords) {
		let statsMap = byTimestamp.get(rec.created)
		if (!statsMap) {
			statsMap = {}
			byTimestamp.set(rec.created, statsMap)
		}
		const key = byLocation ? locationSeriesKey(rec.monitor, rec.system ?? "") : rec.monitor
		statsMap[key] = getMonitorStats(rec)
	}
	return Array.from(byTimestamp.entries())
		.sort(([a], [b]) => a - b)
		.map(([created, stats]) => ({ created, stats }))
}

/** Filter of a monitor's stats records of a type, only of one location when scope.location is set. */
function monitorStatsFilter(monitorId: string, type: string, scope: MonitorStatsScope, created?: string | number) {
	const params: Record<string, string | number> = { id: monitorId, type, system: scope.location ?? "" }
	let filter = "monitor={:id} && type={:type}"
	if (scope.location !== undefined) filter += " && system={:system}"
	if (created !== undefined) {
		filter += " && created>{:created}"
		params.created = created
	}
	return pb.filter(filter, params)
}

/** Fetch stats for one monitor and time range, returning merged chart records. */
async function fetchMonitorStats(
	monitorId: string,
	chartTime: ChartTimes,
	scope: MonitorStatsScope,
	cached?: NetworkMonitorStatsRecord[]
): Promise<NetworkMonitorStatsRecord[]> {
	const lastCached = cached?.at(-1)?.created as number | undefined
	const rawRecords = await pb.collection<RawMonitorStatsRecord>("network_monitor_stats").getFullList({
		filter: monitorStatsFilter(
			monitorId,
			chartTimeData[chartTime].type,
			scope,
			getPbTimestamp(chartTime, lastCached ? new Date(lastCached + 1000) : undefined, true)
		),
		fields: "monitor,system,res_min,res_max,total_count,success_count,res_sum,created",
		sort: "created",
	})
	return mergeMonitorStats(rawRecords, scope.byLocation)
}

const NETWORK_MONITOR_FIELDS = [
	"id,system,locations,quorum,locationStatus,users,name,target,protocol,port,server,interval,timeout,retries,retryInterval",
	"http,httpSecrets,notify,certExpiryDays,pushToken,enabled",
	"res,resMin1h,resMax1h,resAvg1h,loss1h,certInfo,updated",
	"status,statusChanged,lastCheck,lastError,lastStatusCode,recent,uptime",
	"dependsOn,suppressedBy",
	"managedBy,managedKey,managedFields",
].join(",")

interface UseNetworkMonitorsProps {
	systemId?: string
}

export function useNetworkMonitors(props: UseNetworkMonitorsProps) {
	const { systemId } = props

	const [monitors, setMonitors] = useState<NetworkMonitorRecord[]>([])
	const [isLoading, setIsLoading] = useState(true)
	const pendingMonitorEvents = useRef(new Map<string, RecordSubscription<NetworkMonitorRecord>>())
	const monitorBatchTimeout = useRef<ReturnType<typeof setTimeout> | null>(null)

	// initial load
	useEffect(() => {
		let cancelled = false
		setIsLoading(true)
		setMonitors([])
		fetchMonitors(systemId).then((monitors) => {
			if (cancelled) return
			setMonitors(monitors)
			setIsLoading(false)
		})
		return () => {
			cancelled = true
		}
	}, [systemId])

	// subscribe to updates
	useEffect(() => {
		let unsubscribe: (() => void) | undefined
		let cancelled = false

		function flushPendingMonitorEvents() {
			monitorBatchTimeout.current = null
			if (!pendingMonitorEvents.current.size) {
				return
			}
			const events = pendingMonitorEvents.current
			pendingMonitorEvents.current = new Map()
			setMonitors((currentMonitors) => {
				return applyMonitorEvents(currentMonitors ?? [], events.values(), systemId)
			})
		}

		const pbOptions: RecordListOptions = { fields: NETWORK_MONITOR_FIELDS }
		if (systemId) {
			pbOptions.filter = systemMonitorsFilter(systemId)
		}

		;(async () => {
			try {
				const unsub = await pb.collection<NetworkMonitorRecord>("network_monitors").subscribe(
					"*",
					(event) => {
						if (cancelled) return
						pendingMonitorEvents.current.set(event.record.id, event)
						if (!monitorBatchTimeout.current) {
							monitorBatchTimeout.current = setTimeout(flushPendingMonitorEvents, 50)
						}
					},
					pbOptions
				)
				// the effect may have been cleaned up while subscribing
				if (cancelled) unsub()
				else unsubscribe = unsub
			} catch (error) {
				console.error("Failed to subscribe to monitors", error)
			}
		})()

		return () => {
			cancelled = true
			if (monitorBatchTimeout.current !== null) {
				clearTimeout(monitorBatchTimeout.current)
				monitorBatchTimeout.current = null
			}
			pendingMonitorEvents.current.clear()
			unsubscribe?.()
		}
	}, [systemId])

	return { monitors, isLoading }
}

interface UseNetworkMonitorStatsProps extends MonitorStatsScope {
	/** System whose agent streams realtime stats (1m chart time). */
	systemId: string
	monitorId: string
	/** Monitor probe interval in seconds, used to tell missing data apart from slow probes */
	interval: number
	chartTime: ChartTimes
	enabled?: boolean
}

/**
 * Returns the monitor's stats with empty records inserted where data is missing (see withMonitorGaps).
 * With location, only that location's stats ("" for the hub); with byLocation, one series per
 * location (keyed by locationSeriesKey, without gap records).
 */
export function useNetworkMonitorStats(props: UseNetworkMonitorStatsProps) {
	const { systemId, monitorId: id, interval, chartTime, enabled = true, location, byLocation = false } = props
	// cache and state are kept per monitor and scope
	const monitorId = statsKey(id, { location, byLocation })
	const scopeRef = useRef<MonitorStatsScope>({ location, byLocation })
	scopeRef.current = { location, byLocation }
	const [monitorStats, setMonitorStats] = useState<NetworkMonitorStatsRecord[]>([])
	// pending raw events to be merged (keyed by monitor+created)
	const pendingRaw = useRef(new Map<string, RawMonitorStatsRecord>())
	const mergeBatchTimeout = useRef<ReturnType<typeof setTimeout> | null>(null)

	useEffect(() => {
		setMonitorStats(getCacheValue(monitorId, chartTime === "1m" ? "rt" : chartTime))
	}, [monitorId, chartTime])

	// Fetch only the selected monitor's missing history.
	useEffect(() => {
		if (!enabled || chartTime === "1m") {
			return
		}

		let cancelled = false
		const { expectedInterval } = chartTimeData[chartTime]
		const cachedMonitorStats = getCacheValue(monitorId, chartTime)

		if (cachedMonitorStats.length) {
			setMonitorStats(cachedMonitorStats)
			const lastCreated = cachedMonitorStats.at(-1)?.created
			if (lastCreated && Date.now() - lastCreated < expectedInterval * 0.9) {
				return
			}
		}

		fetchMonitorStats(id, chartTime, scopeRef.current, cachedMonitorStats)
			.then((newMonitorStats) => {
				if (cancelled) return
				setMonitorStats(appendCacheValue(monitorId, chartTime, newMonitorStats))
			})
			.catch((error) => {
				if (!cancelled) console.error("Failed to fetch monitor stats:", error)
			})
		return () => {
			cancelled = true
		}
	}, [monitorId, chartTime, enabled])

	// subscribe to new per-monitor stats records; batch them into merged chart records
	useEffect(() => {
		if (!enabled || chartTime === "1m") {
			return
		}
		let cancelled = false
		let unsubscribe: (() => void) | undefined
		const scope = scopeRef.current
		const pbOptions = {
			fields: "monitor,system,res_min,res_max,total_count,success_count,res_sum,created,type",
			filter: monitorStatsFilter(id, chartTimeData[chartTime].type, scope),
		}

		function flushPending() {
			mergeBatchTimeout.current = null
			const pending = pendingRaw.current
			pendingRaw.current = new Map()
			const merged = mergeMonitorStats(Array.from(pending.values()), scope.byLocation)
			if (merged.length > 0) {
				const newStats = appendCacheValue(monitorId, chartTime, merged)
				setMonitorStats(newStats)
			}
		}

		;(async () => {
			try {
				unsubscribe = await pb.collection<RawMonitorStatsRecord>("network_monitor_stats").subscribe(
					"*",
					(event) => {
						if (cancelled || event.action !== "create") {
							return
						}
						const rec = event.record
						pendingRaw.current.set(`${rec.monitor}:${rec.system ?? ""}:${rec.created}`, rec)
						if (!mergeBatchTimeout.current) {
							mergeBatchTimeout.current = setTimeout(flushPending, 200)
						}
					},
					pbOptions
				)
				if (cancelled) unsubscribe()
			} catch (error) {
				console.error("Failed to subscribe to monitor stats:", error)
			}
		})()

		return () => {
			cancelled = true
			if (mergeBatchTimeout.current) {
				clearTimeout(mergeBatchTimeout.current)
				mergeBatchTimeout.current = null
			}
			pendingRaw.current.clear()
			unsubscribe?.()
		}
	}, [monitorId, chartTime, enabled])

	// subscribe to realtime metrics if chart time is 1m
	useEffect(() => {
		if (!enabled || chartTime !== "1m") {
			return
		}
		let cancelled = false
		let unsubscribe: (() => void) | undefined
		pb.realtime
			.subscribe(
				`rt_metrics`,
				(data: { Monitors: NetworkMonitorStatsRecord["stats"] }) => {
					const monitorStats = data.Monitors?.[id]
					if (cancelled || !monitorStats) return
					const key = scopeRef.current.byLocation ? locationSeriesKey(id, systemId) : id
					const stats = { created: Date.now(), stats: { [key]: clearFailedResponse(monitorStats) } }
					const newStats = appendCacheValue(monitorId, "rt", [stats], 120)
					setMonitorStats(newStats)
				},
				{ query: { system: systemId } }
			)
			.then((us) => {
				unsubscribe = us
				if (cancelled) unsubscribe()
			})
		return () => {
			cancelled = true
			unsubscribe?.()
		}
	}, [chartTime, systemId, monitorId, enabled])

	return useMemo(
		() =>
			byLocation
				? monitorStats.filter((record) => record.created != null)
				: withMonitorGaps(monitorStats, { id, interval }, chartTimeData[chartTime].expectedInterval),
		[monitorStats, id, interval, chartTime, byLocation]
	)
}

/** Filter of the monitors a system runs: those with the system as their primary or any location. */
function systemMonitorsFilter(system: string) {
	return pb.filter("system = {:system} || locationSystems.id ?= {:system}", { system })
}

/** Whether a monitor runs on a system, as its primary or any other location. */
export function monitorRunsOn(monitor: Pick<NetworkMonitorRecord, "system" | "locations">, systemId: string) {
	return monitor.system === systemId || getMonitorLocations(monitor).includes(systemId)
}

async function fetchMonitors(system?: string) {
	try {
		return await pb.collection<NetworkMonitorRecord>("network_monitors").getFullList({
			fields: NETWORK_MONITOR_FIELDS,
			filter: system ? systemMonitorsFilter(system) : undefined,
		})
	} catch (error) {
		toast({
			title: "Error",
			description: (error as Error)?.message,
			variant: "destructive",
		})
		return []
	}
}

function applyMonitorEvents(
	monitors: NetworkMonitorRecord[],
	events: Iterable<RecordSubscription<NetworkMonitorRecord>>,
	systemId?: string
) {
	const monitorById = new Map(monitors.map((monitor) => [monitor.id, monitor]))
	const createdMonitors: NetworkMonitorRecord[] = []

	for (const { action, record } of events) {
		const matchesSystemScope = !systemId || monitorRunsOn(record, systemId)

		if (action === "delete" || !matchesSystemScope) {
			monitorById.delete(record.id)
			continue
		}

		if (!monitorById.has(record.id)) {
			createdMonitors.push(record)
		}

		monitorById.set(record.id, record)
	}

	const nextMonitors: NetworkMonitorRecord[] = []
	for (let index = createdMonitors.length - 1; index >= 0; index -= 1) {
		nextMonitors.push(createdMonitors[index])
	}

	for (const monitor of monitors) {
		const nextMonitor = monitorById.get(monitor.id)
		if (!nextMonitor) {
			continue
		}
		nextMonitors.push(nextMonitor)
		monitorById.delete(monitor.id)
	}

	return nextMonitors
}

const DOWN_MONITOR_FIELDS =
	"id,system,name,target,protocol,port,status,statusChanged,lastError,lastStatusCode,notify,enabled"

/**
 * Enabled monitors with notifications that are currently down. Refetches when a
 * monitor changes status (a monitor_events record is written), which is much
 * less frequent than monitor record updates.
 */
export function useDownMonitors() {
	const [monitors, setMonitors] = useState<NetworkMonitorRecord[]>([])

	useEffect(() => {
		let cancelled = false
		let unsubscribe: (() => void) | undefined
		let refetchTimeout: ReturnType<typeof setTimeout> | undefined

		const fetchDown = () =>
			pb
				.collection<NetworkMonitorRecord>("network_monitors")
				.getFullList({
					fields: DOWN_MONITOR_FIELDS,
					filter: 'status = "down" && notify = true && enabled = true',
				})
				.then((records) => {
					if (!cancelled) setMonitors(records)
				})
				.catch((error) => {
					if (!cancelled) console.error("Failed to fetch down monitors", error)
				})

		fetchDown()
		;(async () => {
			try {
				const unsub = await pb.collection("monitor_events").subscribe(
					"*",
					() => {
						if (cancelled) return
						clearTimeout(refetchTimeout)
						refetchTimeout = setTimeout(fetchDown, 1000)
					},
					{ fields: "id" }
				)
				// the effect may have been cleaned up while subscribing
				if (cancelled) unsub()
				else unsubscribe = unsub
			} catch (error) {
				console.error("Failed to subscribe to monitor events", error)
			}
		})()

		return () => {
			cancelled = true
			clearTimeout(refetchTimeout)
			unsubscribe?.()
		}
	}, [])

	return monitors
}
