import { t } from "@lingui/core/macro"
import { isDockerManaged, managedContainer } from "@/lib/docker-discovery"
import type { NetworkMonitorRecord } from "@/types"
import { Badge } from "../ui/badge"
import { DockerIcon } from "../ui/icons"

/** Marks a monitor created from Docker container labels, naming the container. */
export function DockerManagedBadge({
	monitor,
}: {
	monitor: Pick<NetworkMonitorRecord, "managedBy" | "managedKey" | "managedFields">
}) {
	if (!isDockerManaged(monitor)) {
		return null
	}
	const container = managedContainer(monitor)
	return (
		<Badge
			variant="outline"
			className="shrink-0 gap-1 px-1.5 font-normal max-w-32"
			title={t`Managed by Docker labels on ${container}`}
		>
			<DockerIcon className="size-3 shrink-0" />
			<span className="truncate">{container}</span>
		</Badge>
	)
}
