import { Trans } from "@lingui/react/macro"
import { ExternalLinkIcon } from "lucide-react"
import { useEffect, useState } from "react"
import { SwitchField } from "@/components/network-monitors-table/monitor-http-options"
import { pb } from "@/lib/api"
import { dockerLabelsGuideUrl } from "@/lib/docker-discovery"
import type { DiscoveryError, SystemRecord } from "@/types"

type DiscoveryFields = Pick<SystemRecord, "autoDiscover" | "autoDiscoverTraefik" | "discoveryErrors">

/**
 * Loads the discovery settings of a system (the systems store omits them) and
 * returns them with setters. `data()` returns the fields to save, empty until loaded.
 */
export function useDockerDiscovery(systemId?: string) {
	const [loaded, setLoaded] = useState(!systemId)
	const [autoDiscover, setAutoDiscover] = useState(false)
	const [traefik, setTraefik] = useState(false)
	const [errors, setErrors] = useState<DiscoveryError[] | null>(null)

	useEffect(() => {
		if (!systemId) {
			return
		}
		let cancelled = false
		pb.collection("systems")
			.getOne<DiscoveryFields>(systemId, { fields: "autoDiscover,autoDiscoverTraefik,discoveryErrors" })
			.then((record) => {
				if (cancelled) {
					return
				}
				setAutoDiscover(!!record.autoDiscover)
				setTraefik(!!record.autoDiscoverTraefik)
				setErrors(record.discoveryErrors ?? null)
				setLoaded(true)
			})
			.catch((e) => console.error(e))
		return () => {
			cancelled = true
		}
	}, [systemId])

	const data = (): DiscoveryFields => (loaded ? { autoDiscover, autoDiscoverTraefik: autoDiscover && traefik } : {})

	return { loaded, autoDiscover, setAutoDiscover, traefik, setTraefik, errors, data }
}

/** Docker label discovery toggles and errors of the system dialog. */
export function DockerDiscoverySettings({
	loaded,
	autoDiscover,
	setAutoDiscover,
	traefik,
	setTraefik,
	errors,
}: ReturnType<typeof useDockerDiscovery>) {
	return (
		<div className="grid gap-3 mb-4">
			<SwitchField
				id="system-auto-discover"
				checked={autoDiscover}
				onCheckedChange={setAutoDiscover}
				disabled={!loaded}
				label={<Trans>Create monitors from Docker labels</Trans>}
				description={
					<>
						<Trans>
							Containers with <code className="bg-muted px-1 rounded-sm">infrascope.monitor.*</code> labels get
							monitors. Requires agent 0.21.0 or newer.
						</Trans>{" "}
						<a
							href={dockerLabelsGuideUrl}
							target="_blank"
							rel="noopener"
							className="link inline-flex items-center gap-0.5"
						>
							<Trans>Label reference</Trans>
							<ExternalLinkIcon className="size-3" />
						</a>
					</>
				}
			/>
			{autoDiscover && (
				<SwitchField
					id="system-auto-discover-traefik"
					checked={traefik}
					onCheckedChange={setTraefik}
					label={<Trans>Also monitor Traefik router hosts</Trans>}
					description={<Trans>Each Host rule of a Traefik router becomes an HTTP monitor.</Trans>}
				/>
			)}
			{autoDiscover && errors && errors.length > 0 && (
				<div className="grid gap-1 rounded-md border border-destructive/50 p-2 text-xs">
					<p className="font-medium text-destructive">
						<Trans>Discovery errors</Trans>
					</p>
					<ul className="grid gap-0.5 max-h-32 overflow-y-auto">
						{errors.map((error) => (
							<li key={`${error.container}:${error.key}`} className="break-words">
								<span className="font-mono">
									{error.container}
									{error.key !== "default" && `/${error.key}`}
								</span>
								: {error.error}
							</li>
						))}
					</ul>
				</div>
			)}
		</div>
	)
}
