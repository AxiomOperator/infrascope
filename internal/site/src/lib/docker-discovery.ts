import type { NetworkMonitorRecord } from "@/types"

/** Guide describing the Docker label scheme. */
export const dockerLabelsGuideUrl =
	"https://github.com/AxiomOperator/infrascope/blob/main/supplemental/guides/docker-label-monitors.md"

/** Label that stops a container from declaring monitors. */
export const disableLabel = "infrascope.monitor.enable=false"

type ManagedMonitor = Pick<NetworkMonitorRecord, "managedBy" | "managedKey" | "managedFields">

/** Reports whether a monitor was created from Docker container labels. */
export function isDockerManaged(monitor?: ManagedMonitor | null): boolean {
	return monitor?.managedBy === "docker"
}

/** Returns the container that declares a managed monitor, from its key "docker:<container>:<id>". */
export function managedContainer(monitor?: ManagedMonitor | null): string {
	if (!isDockerManaged(monitor)) {
		return ""
	}
	const [, container = ""] = (monitor?.managedKey ?? "").split(":")
	return container
}

/**
 * Reports whether a field of a managed monitor is set by labels and therefore read-only.
 * Fields: name, protocol, target, port, interval, retries, timeout, notify, locations, keyword, acceptedCodes.
 */
export function isManagedField(monitor: ManagedMonitor | null | undefined, field: string): boolean {
	return isDockerManaged(monitor) && (monitor?.managedFields ?? []).includes(field)
}
