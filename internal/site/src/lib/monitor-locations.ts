import type { MonitorLocationStatus, MonitorStatus, NetworkMonitorRecord } from "@/types"

/** Location of monitors checked by the hub itself. */
export const HUB_LOCATION = "hub"

/** Largest number of locations of a monitor (enforced by the hub). */
export const MAX_MONITOR_LOCATIONS = 10

type LocatedMonitor = Pick<NetworkMonitorRecord, "system"> & Partial<Pick<NetworkMonitorRecord, "locations">>

/**
 * Locations of a monitor: its stored locations, or, for monitors stored before
 * multi-location checks, the hub or its system.
 */
export function getMonitorLocations(monitor: LocatedMonitor): string[] {
	const locations = Array.isArray(monitor.locations) ? monitor.locations.filter(Boolean) : []
	if (locations.length) return [...new Set(locations)]
	return [monitor.system || HUB_LOCATION]
}

/** Whether a monitor is checked from more than one location. */
export function isMultiLocation(monitor: LocatedMonitor) {
	return getMonitorLocations(monitor).length > 1
}

/** Default quorum of n locations: a majority. */
export function defaultQuorum(n: number) {
	return n <= 1 ? 1 : Math.floor(n / 2) + 1
}

/** Effective quorum of a stored value for n locations, like the hub: the value when in 1..n, else the default. */
export function effectiveQuorum(quorum: number | undefined, n: number) {
	if (!quorum || quorum < 1 || quorum > Math.max(n, 1)) return defaultQuorum(n)
	return quorum
}

/** Primary location of locations: the first agent system, or "" when only the hub runs the monitor. */
export function primaryLocationSystem(locations: Iterable<string>) {
	for (const location of locations) {
		if (location !== HUB_LOCATION) return location
	}
	return ""
}

/** Agent systems among locations, in order. */
export function locationSystemIds(locations: Iterable<string>) {
	return [...locations].filter((location) => location !== HUB_LOCATION)
}

/** System id of a location as stored in network_monitor_stats.system: "" for the hub. */
export function locationStatsSystem(location: string) {
	return location === HUB_LOCATION ? "" : location
}

/** Display name of a location: hubName for the hub, else the system name (or id when unknown). */
export function getLocationName(
	location: string,
	systemNames: Record<string, { name: string } | undefined>,
	hubName: string
) {
	if (location === HUB_LOCATION) return hubName
	return systemNames[location]?.name || location
}

export type MonitorLocationState = MonitorLocationStatus & { location: string }

/** Status of each location of a monitor, in location order; locations without a status are unknown. */
export function getLocationStatuses(
	monitor: LocatedMonitor & Partial<Pick<NetworkMonitorRecord, "locationStatus" | "enabled">>
): MonitorLocationState[] {
	const stored = monitor.locationStatus && typeof monitor.locationStatus === "object" ? monitor.locationStatus : {}
	return getMonitorLocations(monitor).map((location) => {
		const status = stored[location]
		return {
			...status,
			location,
			status: monitor.enabled === false ? "paused" : ((status?.status || "unknown") as MonitorStatus),
		}
	})
}

/** Counts of location statuses, e.g. how many locations are down. */
export function countLocationStatuses(states: Pick<MonitorLocationState, "status">[]) {
	const counts: Partial<Record<MonitorStatus, number>> = {}
	for (const { status } of states) {
		counts[status] = (counts[status] ?? 0) + 1
	}
	return counts
}

/** Moves the hub to the front of locations and removes duplicates; order of systems is kept. */
export function orderLocations(locations: Iterable<string>) {
	const unique = [...new Set(locations)]
	return unique.includes(HUB_LOCATION) ? [HUB_LOCATION, ...unique.filter((l) => l !== HUB_LOCATION)] : unique
}
