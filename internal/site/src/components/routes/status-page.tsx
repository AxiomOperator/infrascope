import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import {
	AlertTriangleIcon,
	CheckCircle2Icon,
	CircleHelpIcon,
	LoaderCircleIcon,
	WrenchIcon,
	XCircleIcon,
} from "lucide-react"
import { type ReactNode, useEffect, useState } from "react"
import {
	IncidentImpactBadge,
	IncidentStatusBadge,
	IncidentTimeline,
	incidentImpactCardColors,
} from "@/components/incidents/incident-ui"
import { ModeToggle } from "@/components/mode-toggle"
import { monitorStatusLabel } from "@/components/network-monitors-table/monitor-status-badge"
import { prependBasePath } from "@/components/router"
import {
	ComponentGroup,
	StatusPageFooter,
	StatusPageLogo,
	useAccentStyle,
	useFeedLinks,
} from "@/components/status-page-branding"
import { StatusSubscribeButton } from "@/components/status-subscribe"
import { Badge } from "@/components/ui/badge"
import { HoverBars } from "@/components/ui/hover-bars"
import { pb } from "@/lib/api"
import { formatIncidentDuration } from "@/lib/incidents"
import { layoutComponents } from "@/lib/status-page-branding"
import { formatRelativeTime, formatUptime, monitorStatusBadgeColors } from "@/lib/network-monitor-utils"
import { cn, formatShortDate } from "@/lib/utils"
import type {
	PublicStatusPage,
	PublicStatusPageDay,
	PublicStatusPageIncident,
	PublicStatusPageMaintenance,
	PublicStatusPageMonitor,
	PublicStatusPageSystem,
} from "@/types"

const POLL_INTERVAL = 60_000
const MAX_BACKOFF = 15 * 60_000

type LoadResult =
	| { kind: "ok"; data: PublicStatusPage }
	| { kind: "notfound" }
	| { kind: "ratelimited"; retryAfter: number }
	| { kind: "error" }

/**
 * Fetch a status page without going through the PocketBase client, so no
 * authenticated endpoints or auth refreshes are involved. The auth token is only
 * sent when valid, which lets owners preview pages that are not public.
 */
async function loadStatusPage(slug: string, signal: AbortSignal): Promise<LoadResult> {
	const headers: HeadersInit = { Accept: "application/json" }
	if (pb.authStore.isValid && pb.authStore.token) {
		headers.Authorization = pb.authStore.token
	}
	try {
		const res = await fetch(prependBasePath(`/api/beszel/status-pages/${encodeURIComponent(slug)}`), {
			headers,
			signal,
			cache: "no-store",
		})
		if (res.status === 404) return { kind: "notfound" }
		if (res.status === 429) {
			const retryAfter = Number(res.headers.get("Retry-After")) * 1000
			return { kind: "ratelimited", retryAfter: Number.isFinite(retryAfter) ? retryAfter : 0 }
		}
		if (!res.ok) return { kind: "error" }
		return { kind: "ok", data: (await res.json()) as PublicStatusPage }
	} catch (error) {
		if (signal.aborted) throw error
		return { kind: "error" }
	}
}

/** Poll a status page every minute while the document is visible, backing off on errors. */
function useStatusPage(slug: string) {
	const [data, setData] = useState<PublicStatusPage | null>(null)
	const [problem, setProblem] = useState<"notfound" | "ratelimited" | "error" | null>(null)
	const [loading, setLoading] = useState(true)

	useEffect(() => {
		let cancelled = false
		let timer: ReturnType<typeof setTimeout> | undefined
		let controller: AbortController | undefined
		let backoff = 0
		let nextDue = 0
		let stopped = false

		setData(null)
		setProblem(null)
		setLoading(true)

		const schedule = (delay: number) => {
			nextDue = Date.now() + delay
			clearTimeout(timer)
			if (!document.hidden) timer = setTimeout(run, delay)
		}

		const retryLater = (minDelay = 0) => {
			backoff = Math.min(backoff ? backoff * 2 : POLL_INTERVAL, MAX_BACKOFF)
			schedule(Math.max(backoff, minDelay))
		}

		async function run() {
			// resumed by the visibility handler
			if (cancelled || document.hidden) return
			controller?.abort()
			controller = new AbortController()
			let result: LoadResult
			try {
				result = await loadStatusPage(slug, controller.signal)
			} catch {
				return // aborted
			}
			if (cancelled) return
			setLoading(false)
			switch (result.kind) {
				case "ok":
					backoff = 0
					setData(result.data)
					setProblem(null)
					schedule(POLL_INTERVAL)
					break
				case "notfound":
					stopped = true
					setData(null)
					setProblem("notfound")
					break
				case "ratelimited":
					setProblem("ratelimited")
					retryLater(result.retryAfter)
					break
				default:
					setProblem("error")
					retryLater()
			}
		}

		const onVisibilityChange = () => {
			if (stopped) return
			if (document.hidden) {
				clearTimeout(timer)
			} else {
				clearTimeout(timer)
				timer = setTimeout(run, Math.max(0, nextDue - Date.now()))
			}
		}

		document.addEventListener("visibilitychange", onVisibilityChange)
		run()

		return () => {
			cancelled = true
			clearTimeout(timer)
			controller?.abort()
			document.removeEventListener("visibilitychange", onVisibilityChange)
		}
	}, [slug])

	return { data, problem, loading }
}

/** Re-render periodically so relative times stay current. */
function useNow(interval = 15_000) {
	const [now, setNow] = useState(() => Date.now())
	useEffect(() => {
		const id = setInterval(() => setNow(Date.now()), interval)
		return () => clearInterval(id)
	}, [interval])
	return now
}

export default function StatusPage({ slug }: { slug: string }) {
	const { data, problem, loading } = useStatusPage(slug)
	const accentStyle = useAccentStyle(data?.branding?.accentColor)
	useFeedLinks(slug, data?.title)

	useEffect(() => {
		if (data?.title) document.title = data.title
		else if (problem === "notfound") document.title = t`Status page not found`
	}, [data?.title, problem])

	let content: ReactNode
	if (data) {
		content = <StatusPageContent slug={slug} data={data} rateLimited={problem === "ratelimited"} />
	} else if (problem === "notfound") {
		content = (
			<Message
				title={<Trans>Status page not found</Trans>}
				description={<Trans>This status page does not exist or is not public.</Trans>}
			/>
		)
	} else if (problem === "ratelimited") {
		content = <Message title={<Trans>Too many requests, retrying…</Trans>} />
	} else if (problem === "error") {
		content = (
			<Message title={<Trans>Failed to load status page</Trans>} description={<Trans>Retrying automatically.</Trans>} />
		)
	} else if (loading) {
		content = (
			<div className="flex justify-center py-24">
				<LoaderCircleIcon className="size-6 animate-spin text-muted-foreground" />
				<span className="sr-only">
					<Trans>Loading…</Trans>
				</span>
			</div>
		)
	}

	return (
		<div className="min-h-dvh flex flex-col bg-background text-foreground" style={accentStyle}>
			{data?.branding?.accentColor && <div className="h-1 bg-(--sp-accent)" />}
			<main className="mx-auto w-full max-w-4xl flex-1 px-4 pt-6 pb-10 sm:pt-10">{content}</main>
			<StatusPageFooter slug={slug} branding={data?.branding} showFeeds={!!data} />
		</div>
	)
}

function Message({ title, description }: { title: ReactNode; description?: ReactNode }) {
	return (
		<div className="relative py-24 text-center">
			<div className="absolute end-0 top-0">
				<ModeToggle />
			</div>
			<h1 className="text-2xl font-semibold">{title}</h1>
			{description && <p className="mt-2 text-muted-foreground">{description}</p>}
		</div>
	)
}

const overallConfig: Record<
	PublicStatusPage["overall"],
	{ label: () => string; icon: typeof CheckCircle2Icon; className: string }
> = {
	up: {
		label: () => t`All systems operational`,
		icon: CheckCircle2Icon,
		className: "border-green-500/30 bg-green-500/10 text-green-700 dark:text-green-400",
	},
	degraded: {
		label: () => t`Partial outage`,
		icon: AlertTriangleIcon,
		className: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-400",
	},
	down: {
		label: () => t`Major outage`,
		icon: XCircleIcon,
		className: "border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-400",
	},
	maintenance: {
		label: () => t`Under maintenance`,
		icon: WrenchIcon,
		className: "border-blue-500/30 bg-blue-500/10 text-blue-700 dark:text-blue-400",
	},
	unknown: {
		label: () => t`Status unknown`,
		icon: CircleHelpIcon,
		className: "border-border bg-muted/50 text-muted-foreground",
	},
}

function StatusPageContent({
	slug,
	data,
	rateLimited,
}: {
	slug: string
	data: PublicStatusPage
	rateLimited: boolean
}) {
	const now = useNow()
	const overall = overallConfig[data.overall] ?? overallConfig.unknown
	const OverallIcon = overall.icon
	const maintenance = data.maintenance ?? []
	const monitors = data.monitors ?? []
	const systems = data.systems ?? []
	const layout = layoutComponents(data)
	const activeIncidents = data.incidents?.active ?? []
	const pastIncidents = data.incidents?.recent ?? []

	return (
		<div className="grid gap-6">
			<header className="flex items-start justify-between gap-4">
				<div className="min-w-0">
					<StatusPageLogo logo={data.branding?.logo} />
					<h1 className="text-2xl sm:text-3xl font-semibold tracking-tight break-words">{data.title}</h1>
					{data.description && (
						<p className="mt-2 text-muted-foreground whitespace-pre-line break-words">{data.description}</p>
					)}
				</div>
				<div className="flex shrink-0 items-center gap-2">
					{data.subscriptions && <StatusSubscribeButton slug={slug} title={data.title} />}
					<ModeToggle />
				</div>
			</header>

			<section
				className={cn("flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border px-4 py-3.5", overall.className)}
			>
				<div className="flex items-center gap-2.5 me-auto">
					<OverallIcon className="size-5 shrink-0" />
					<h2 className="text-base sm:text-lg font-semibold">{overall.label()}</h2>
				</div>
				<span
					className="text-xs opacity-80"
					title={data.updated ? formatShortDate(new Date(data.updated).toISOString()) : undefined}
				>
					{rateLimited ? (
						<Trans>Too many requests, retrying…</Trans>
					) : data.updated ? (
						<Trans>Updated {formatRelativeTime(data.updated, now)}</Trans>
					) : null}
				</span>
			</section>

			{activeIncidents.length > 0 && (
				<section className="grid gap-3" aria-label={t`Active incidents`}>
					{activeIncidents.map((incident, i) => (
						<IncidentCard key={`${incident.startedAt}-${i}`} incident={incident} now={now} />
					))}
				</section>
			)}

			{maintenance.length > 0 && (
				<section className="grid gap-3" aria-label={t`Maintenance`}>
					{maintenance.map((item, i) => (
						<MaintenanceNotice key={`${item.start}-${i}`} item={item} />
					))}
				</section>
			)}

			{layout.groups.map((group, g) => (
				<ComponentGroup key={`${group.name}-${g}`} group={group} count={group.items.length}>
					{group.items.map(({ kind, index, item }) => (
						<ComponentRow
							key={`${kind}-${index}`}
							item={item}
							showResponseTimes={kind === "monitor" && data.showResponseTimes}
						/>
					))}
				</ComponentGroup>
			))}

			{layout.systems.length > 0 && (
				<ComponentSection title={t`Servers`}>
					{layout.systems.map((i) => (
						<ComponentRow key={`${systems[i].name}-${i}`} item={systems[i]} showResponseTimes={false} />
					))}
				</ComponentSection>
			)}

			{layout.monitors.length > 0 && (
				<ComponentSection title={layout.systems.length > 0 || layout.groups.length > 0 ? t`Monitors` : undefined}>
					{layout.monitors.map((i) => (
						<ComponentRow
							key={`${monitors[i].name}-${i}`}
							item={monitors[i]}
							showResponseTimes={data.showResponseTimes}
						/>
					))}
				</ComponentSection>
			)}

			{systems.length === 0 && monitors.length === 0 && (
				<p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
					<Trans>No systems or monitors on this page.</Trans>
				</p>
			)}

			{data.incidents && (
				<section className="grid gap-2" aria-labelledby="past-incidents">
					<h2 id="past-incidents" className="text-sm font-semibold text-(--sp-accent-muted) px-1">
						<Trans>Past incidents</Trans>
					</h2>
					{pastIncidents.length === 0 ? (
						<p className="rounded-lg border border-dashed px-4 py-5 text-center text-sm text-muted-foreground">
							<Trans>No incidents in the last 14 days.</Trans>
						</p>
					) : (
						<div className="grid gap-3">
							{pastIncidents.map((incident, i) => (
								<IncidentCard key={`${incident.startedAt}-${i}`} incident={incident} now={now} />
							))}
						</div>
					)}
				</section>
			)}
		</div>
	)
}

/** An incident with its updates. Title and messages are plain text. */
function IncidentCard({ incident, now }: { incident: PublicStatusPageIncident; now: number }) {
	const resolved = incident.status === "resolved"
	const started = new Date(incident.startedAt).getTime()
	const end = resolved && incident.resolvedAt ? new Date(incident.resolvedAt).getTime() : now
	const duration = Number.isNaN(started) ? "" : formatIncidentDuration(end - started)
	return (
		<article
			className={cn(
				"rounded-lg border px-4 py-3 grid gap-3",
				resolved ? "border-border/60 bg-card" : incidentImpactCardColors[incident.impact]
			)}
		>
			<div className="grid gap-1">
				<div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
					{!resolved && <AlertTriangleIcon className="size-4 shrink-0 text-amber-600 dark:text-amber-400" />}
					<h3 className="font-semibold break-words min-w-0 me-auto">{incident.title}</h3>
					{!resolved && incident.impact !== "none" && <IncidentImpactBadge impact={incident.impact} />}
					<IncidentStatusBadge status={incident.status} />
				</div>
				<p className="text-xs text-muted-foreground tabular-nums">
					{formatShortDate(incident.startedAt)}
					{resolved && incident.resolvedAt ? ` – ${formatShortDate(incident.resolvedAt)}` : ""}
					{duration && ` · ${duration}`}
				</p>
			</div>
			<IncidentTimeline updates={incident.updates ?? []} />
		</article>
	)
}

function MaintenanceNotice({ item }: { item: PublicStatusPageMaintenance }) {
	return (
		<div className="rounded-lg border border-blue-500/30 bg-blue-500/5 px-4 py-3">
			<div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
				<WrenchIcon className="size-4 shrink-0 text-blue-600 dark:text-blue-400" />
				<h3 className="font-semibold break-words min-w-0">{item.title}</h3>
				<Badge
					className={cn(
						"ms-auto",
						item.active ? "bg-blue-500/15! text-blue-600 dark:text-blue-400" : "bg-muted! text-muted-foreground"
					)}
				>
					{item.active ? <Trans>In progress</Trans> : <Trans>Scheduled</Trans>}
				</Badge>
			</div>
			<p className="mt-1 text-sm text-muted-foreground tabular-nums">
				{formatShortDate(item.start)} – {formatShortDate(item.end)}
			</p>
			{item.description && <p className="mt-1.5 text-sm whitespace-pre-line break-words">{item.description}</p>}
		</div>
	)
}

/** A list of status rows, with an optional heading. */
function ComponentSection({ title, children }: { title?: string; children: ReactNode }) {
	return (
		<section className="grid gap-2" aria-label={title}>
			{title && <h2 className="text-sm font-semibold text-(--sp-accent-muted) px-1">{title}</h2>}
			<div className="rounded-lg border border-border/60 bg-card shadow-xs divide-y">{children}</div>
		</section>
	)
}

/** A system or monitor; systems have no response time or target. */
type ComponentItem = PublicStatusPageSystem | PublicStatusPageMonitor

/** A system or monitor with its status, uptime and daily uptime bars. */
function ComponentRow({ item, showResponseTimes }: { item: ComponentItem; showResponseTimes: boolean }) {
	const { res, target } = item as Partial<PublicStatusPageMonitor>
	const days = item.days ?? []
	const uptime = item.uptime ?? { d1: null, d7: null, d30: null }
	const status = item.status || "unknown"
	return (
		<div className="px-4 py-4 grid gap-3">
			<div className="flex items-start justify-between gap-3">
				<div className="min-w-0">
					<h3 className="font-medium break-words">{item.name}</h3>
					{target && <p className="text-xs text-muted-foreground break-all">{target}</p>}
				</div>
				<Badge className={cn("shrink-0", monitorStatusBadgeColors[status] ?? monitorStatusBadgeColors.unknown)}>
					{monitorStatusLabel(status)}
				</Badge>
			</div>
			<dl className="flex flex-wrap gap-x-5 gap-y-1 text-xs text-muted-foreground">
				<UptimeStat label={t`24h`} value={uptime.d1} />
				<UptimeStat label={t`7d`} value={uptime.d7} />
				<UptimeStat label={t`30d`} value={uptime.d30} />
				{showResponseTimes && res != null && res > 0 && (
					<div className="flex gap-1">
						<dt>
							<Trans>Response</Trans>
						</dt>
						<dd className="font-medium text-foreground tabular-nums">{Math.round(res)} ms</dd>
					</div>
				)}
			</dl>
			{days.length > 0 && (
				<div className="grid gap-1">
					<HoverBars
						items={days}
						label={t`Daily uptime`}
						className="h-7"
						barClassName="rounded-xs"
						getBarClassName={getDayColor}
						renderTooltip={(day) => <DayTooltip day={day} />}
					/>
					<div className="flex justify-between text-[0.7rem] text-(--sp-accent-muted)">
						<span>
							<Trans>{days.length} days ago</Trans>
						</span>
						<span>
							<Trans>Today</Trans>
						</span>
					</div>
				</div>
			)}
		</div>
	)
}

function UptimeStat({ label, value }: { label: string; value: number | null | undefined }) {
	return (
		<div className="flex gap-1">
			<dt>{label}</dt>
			<dd className="font-medium text-foreground tabular-nums">{formatUptime(value)}</dd>
		</div>
	)
}

function getDayColor(day: PublicStatusPageDay) {
	if (day.st === "none") return "bg-muted"
	if (day.st === "maint") return "bg-blue-500"
	if (day.up == null) return day.st === "down" ? "bg-red-500" : "bg-green-500"
	if (day.up >= 99.9) return "bg-green-500"
	if (day.up >= 99) return "bg-lime-500"
	if (day.up >= 95) return "bg-amber-500"
	return "bg-red-500"
}

// days are calendar dates, so format them without a time zone shift
const dayFormatter = new Intl.DateTimeFormat(undefined, {
	weekday: "short",
	month: "short",
	day: "numeric",
	timeZone: "UTC",
})

function formatDay(d: string) {
	const date = new Date(`${d}T00:00:00Z`)
	return Number.isNaN(date.getTime()) ? d : dayFormatter.format(date)
}

function DayTooltip({ day }: { day: PublicStatusPageDay }) {
	return (
		<div className="grid gap-0.5 tabular-nums">
			<span className="text-muted-foreground">{formatDay(day.d)}</span>
			{day.st === "none" ? (
				<span>
					<Trans>No data</Trans>
				</span>
			) : (
				<>
					{day.up != null && (
						<span className="font-medium">
							<Trans>{formatUptime(day.up)} uptime</Trans>
						</span>
					)}
					{day.st === "maint" && (
						<span>
							<Trans>Maintenance</Trans>
						</span>
					)}
					{day.st === "down" && (
						<span>
							<Trans>Outage</Trans>
						</span>
					)}
				</>
			)}
		</div>
	)
}
