/**
 * Dependency-aware alerts: monitors and systems can depend on up to MAX_DEPENDENCIES monitors
 * (dependsOn). While a parent is down, the hub suppresses the record's notifications and sets
 * the server-managed suppressedBy field to the names of the down parents.
 */

/** Most monitors a monitor or system can depend on (matches the hub). */
export const MAX_DEPENDENCIES = 5

type DependencyNode = { id: string; dependsOn?: string[] | null }

/** Names of the down parents in a suppressedBy value, in order. */
export function parseSuppressedBy(suppressedBy: string | null | undefined): string[] {
	if (!suppressedBy) return []
	return suppressedBy
		.split(", ")
		.map((name) => name.trim())
		.filter(Boolean)
}

/**
 * Whether notifications of a record are suppressed and it should be shown as unreachable: a
 * parent is down and the record itself is not up or paused.
 */
export function isUnreachable(record: { status?: string; suppressedBy?: string | null; enabled?: boolean }) {
	if (!record.suppressedBy || record.enabled === false) return false
	return record.status !== "up" && record.status !== "paused"
}

/**
 * Ids of the monitors that depend on monitorId, directly or through other monitors. Selecting
 * any of them as a parent of monitorId would form a cycle.
 */
export function dependentIds(monitors: DependencyNode[], monitorId: string): Set<string> {
	const children = new Map<string, string[]>()
	for (const monitor of monitors) {
		for (const parent of monitor.dependsOn ?? []) {
			const list = children.get(parent)
			if (list) list.push(monitor.id)
			else children.set(parent, [monitor.id])
		}
	}
	const found = new Set<string>()
	const queue = [monitorId]
	while (queue.length) {
		const id = queue.shift() as string
		for (const child of children.get(id) ?? []) {
			if (!found.has(child)) {
				found.add(child)
				queue.push(child)
			}
		}
	}
	return found
}

/**
 * Monitors that can be selected as parents of monitorId (empty for a new monitor or a system):
 * every monitor except itself and those depending on it.
 */
export function dependencyCandidates<T extends DependencyNode>(monitors: T[], monitorId?: string): T[] {
	if (!monitorId) return monitors
	const excluded = dependentIds(monitors, monitorId)
	excluded.add(monitorId)
	return monitors.filter((monitor) => !excluded.has(monitor.id))
}
