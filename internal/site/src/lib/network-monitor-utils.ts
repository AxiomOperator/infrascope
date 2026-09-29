import { t } from "@lingui/core/macro"
import { prependBasePath } from "@/components/router"
import type {
	MonitorCertInfo,
	MonitorRecentCheck,
	MonitorStatus,
	MonitorUptime,
	MonitorStats,
	NetworkMonitorRecord,
	NetworkMonitorStatsRecord,
	RawMonitorStatsRecord,
} from "@/types"
import { usesMonitorPort } from "./monitor-protocols"
import { compareSemVer, parseSemVer, toFixedFloat } from "./utils"

/** Derive chart metrics from the counts and response sum stored at every retention tier. */
export function getMonitorStats(record: RawMonitorStatsRecord): MonitorStats {
	const success = record.success_count > 0
	return {
		res_avg: success ? toFixedFloat(record.res_sum / record.success_count, 2) : null,
		res_min: success ? record.res_min : null,
		res_max: success ? record.res_max : null,
		loss:
			record.total_count > 0
				? toFixedFloat(((record.total_count - record.success_count) / record.total_count) * 100, 2)
				: 0,
	}
}

/**
 * Realtime stats come from the agent without counts and report 0 response times when every
 * probe failed; clear them to match stored stats.
 */
export function clearFailedResponse(stats: MonitorStats): MonitorStats {
	if (stats.loss < 100) return stats
	return { ...stats, res_avg: null, res_min: null, res_max: null }
}

/**
 * Gap marker in the same form appendData uses. Without a timestamp it can't become the active
 * tooltip point, which would otherwise have no values and make the tooltip jump to the corner.
 */
export const monitorGapRecord = { created: null, stats: null } as unknown as NetworkMonitorStatsRecord

/**
 * Return the records that have stats for one monitor, with a gap marker inserted wherever
 * consecutive records are further apart than expected (e.g. while the agent was disconnected),
 * so charts break the line there instead of drawing across the missing time.
 */
export function withMonitorGaps(
	records: NetworkMonitorStatsRecord[],
	monitor: Pick<NetworkMonitorRecord, "id" | "interval">,
	expectedInterval: number
): NetworkMonitorStatsRecord[] {
	// long-interval monitors only get a record when a new probe completes
	const maxGap = Math.max(expectedInterval, monitor.interval * 1000) * 1.5
	const result: NetworkMonitorStatsRecord[] = []
	let prevTime = 0
	for (const record of records) {
		// skip appendData's gap markers (created: null) and records without this monitor
		if (record.created == null || !record.stats?.[monitor.id]) continue
		if (prevTime && record.created - prevTime > maxGap) {
			result.push(monitorGapRecord)
		}
		prevTime = record.created
		result.push(record)
	}
	return result
}

export function getMonitorTarget(monitor: Pick<NetworkMonitorRecord, "target" | "protocol" | "port">) {
	if (!usesMonitorPort(monitor.protocol) || !monitor.port) return monitor.target
	const host = monitor.target.includes(":") && !monitor.target.startsWith("[") ? `[${monitor.target}]` : monitor.target
	return `${host}:${monitor.port}`
}

/** Whole days until the certificate expires; negative once expired. */
export function getCertDaysLeft(cert: Pick<MonitorCertInfo, "expires">, now = Date.now()) {
	return Math.floor((cert.expires - now) / 86_400_000)
}

/** Expiry severity used for certificate colors. */
export function getCertExpiryLevel(daysLeft: number): "ok" | "warning" | "critical" {
	if (daysLeft < 7) return "critical"
	if (daysLeft < 14) return "warning"
	return "ok"
}

/** Agent version that supports HTTP options, timeouts and retry intervals. */
export const MIN_MONITOR_OPTIONS_AGENT_VERSION = parseSemVer("0.21.0")

/** Whether an agent version predates HTTP options, timeouts and retry intervals. */
export function needsAgentUpdateForMonitorOptions(version?: string) {
	return compareSemVer(parseSemVer(version), MIN_MONITOR_OPTIONS_AGENT_VERSION) < 0
}

/** Hub monitors, including push monitors, have no system. */
export function isHubMonitor(monitor: Pick<NetworkMonitorRecord, "system">) {
	return !monitor.system
}

/** Display name of a monitor: its name, or its target when unnamed. */
export function getMonitorName(monitor: Pick<NetworkMonitorRecord, "name" | "target" | "protocol" | "port">) {
	return monitor.name || getMonitorTarget(monitor) || (monitor.protocol === "push" ? t`Push monitor` : "")
}

/** Number of recent checks the backend keeps on a monitor. */
export const MONITOR_RECENT_SLOTS = 60

/** Recent checks of a monitor, oldest first; tolerates missing or malformed values. */
export function getMonitorRecent(monitor: Pick<NetworkMonitorRecord, "recent">): MonitorRecentCheck[] {
	return Array.isArray(monitor.recent) ? monitor.recent : []
}

/** Uptime percentages of a monitor; tolerates missing values. */
export function getMonitorUptime(monitor: Pick<NetworkMonitorRecord, "uptime">): MonitorUptime {
	return monitor.uptime && typeof monitor.uptime === "object" ? monitor.uptime : {}
}

/** Formats an uptime percentage, e.g. 99.95%; "—" without data. */
export function formatUptime(value?: number | null) {
	if (value == null || Number.isNaN(value)) return "—"
	if (value >= 100) return "100%"
	// never round a partial outage up to 100%
	const digits = value >= 99 ? 2 : 1
	const factor = 10 ** digits
	return `${(Math.floor(value * factor) / factor).toFixed(digits)}%`
}

/** Tailwind background color of a monitor status. */
export const monitorStatusBgColors: Record<MonitorStatus | "", string> = {
	up: "bg-green-500",
	down: "bg-red-500",
	pending: "bg-amber-500",
	maintenance: "bg-blue-500",
	paused: "bg-muted-foreground/40",
	unknown: "bg-muted-foreground/40",
	"": "bg-muted-foreground/40",
}

/** Tailwind classes of a status badge. */
export const monitorStatusBadgeColors: Record<MonitorStatus | "", string> = {
	up: "bg-green-500/15! text-green-700 dark:text-green-400",
	down: "bg-red-500/15! text-red-600 dark:text-red-400",
	pending: "bg-amber-500/15! text-amber-600 dark:text-amber-400",
	maintenance: "bg-blue-500/15! text-blue-600 dark:text-blue-400",
	paused: "bg-muted! text-muted-foreground",
	unknown: "bg-muted! text-muted-foreground",
	"": "bg-muted! text-muted-foreground",
}

/** Effective status of a monitor; paused monitors keep their last status in the record. */
export function getMonitorStatus(monitor: Pick<NetworkMonitorRecord, "status" | "enabled">): MonitorStatus {
	if (!monitor.enabled) return "paused"
	return monitor.status || "unknown"
}

/** Full URL that push monitors receive heartbeats on. */
export function getPushUrl(token: string) {
	return `${window.location.origin}${prependBasePath(`/api/beszel/push/${token}`)}`
}

const relativeTimeUnits: [Intl.RelativeTimeFormatUnit, number][] = [
	["year", 31_536_000_000],
	["month", 2_592_000_000],
	["day", 86_400_000],
	["hour", 3_600_000],
	["minute", 60_000],
	["second", 1000],
]

let relativeTimeFormatter: Intl.RelativeTimeFormat | undefined

/** Formats a time relative to now, e.g. "5 minutes ago". */
export function formatRelativeTime(time: string | number | Date, now = Date.now()) {
	const timestamp = new Date(time).getTime()
	if (Number.isNaN(timestamp)) return ""
	relativeTimeFormatter ??= new Intl.RelativeTimeFormat(undefined, { numeric: "auto" })
	const diff = timestamp - now
	for (const [unit, ms] of relativeTimeUnits) {
		if (Math.abs(diff) >= ms || unit === "second") {
			return relativeTimeFormatter.format(Math.round(diff / ms), unit)
		}
	}
	return ""
}

/** Formats a duration in milliseconds, e.g. "2h 5m" or "45s". */
export function formatDurationMs(ms: number) {
	const totalSeconds = Math.max(0, Math.round(ms / 1000))
	const days = Math.floor(totalSeconds / 86_400)
	const hours = Math.floor((totalSeconds % 86_400) / 3600)
	const minutes = Math.floor((totalSeconds % 3600) / 60)
	const seconds = totalSeconds % 60
	if (days) return [`${days}d`, hours ? `${hours}h` : ""].filter(Boolean).join(" ")
	if (hours) return [`${hours}h`, minutes ? `${minutes}m` : ""].filter(Boolean).join(" ")
	if (minutes) return [`${minutes}m`, seconds ? `${seconds}s` : ""].filter(Boolean).join(" ")
	return `${seconds}s`
}
