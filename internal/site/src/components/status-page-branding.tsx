import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { ChevronRightIcon, RssIcon } from "lucide-react"
import { type CSSProperties, type ReactNode, useEffect, useId, useState } from "react"
import { monitorStatusLabel } from "@/components/network-monitors-table/monitor-status-badge"
import { prependBasePath } from "@/components/router"
import { useTheme } from "@/components/theme-provider"
import { Badge } from "@/components/ui/badge"
import { monitorStatusBadgeColors } from "@/lib/network-monitor-utils"
import { accentVariables } from "@/lib/status-page-branding"
import { cn } from "@/lib/utils"
import type { PublicStatusPageBranding, PublicStatusPageGroup } from "@/types"

/**
 * CSS variables of a status page's accent color, set on the page root:
 * --sp-accent (fills, empty without an accent color), --sp-accent-fg (text on
 * fills), --sp-accent-text and --sp-accent-muted (text and links, readable on
 * the page background in the current theme, falling back to the foreground
 * and muted foreground colors).
 */
export function useAccentStyle(accentColor: string | undefined): CSSProperties {
	const { resolvedTheme } = useTheme()
	const vars = accentVariables(accentColor, resolvedTheme === "dark")
	return {
		"--sp-accent": vars?.["--accent-brand"] ?? "transparent",
		"--sp-accent-fg": vars?.["--accent-brand-fg"] ?? "var(--foreground)",
		"--sp-accent-text": vars?.["--accent-brand-text"] ?? "var(--foreground)",
		"--sp-accent-muted": vars?.["--accent-brand-text"] ?? "var(--muted-foreground)",
	} as CSSProperties
}

/** Paths of the feeds of a status page, below the base path. */
export function statusPageFeedPath(slug: string, format: "atom" | "rss") {
	return prependBasePath(`/api/beszel/status-pages/${encodeURIComponent(slug)}/feed.${format}`)
}

/** Add <link rel="alternate"> tags for the incident feeds of a page while it is shown. */
export function useFeedLinks(slug: string, title: string | undefined) {
	useEffect(() => {
		if (!title) return
		const links = (["atom", "rss"] as const).map((format) => {
			const link = document.createElement("link")
			link.rel = "alternate"
			link.type = format === "atom" ? "application/atom+xml" : "application/rss+xml"
			link.title = title
			link.href = statusPageFeedPath(slug, format)
			document.head.appendChild(link)
			return link
		})
		return () => {
			for (const link of links) link.remove()
		}
	}, [slug, title])
}

/** The logo of a status page; hidden when it fails to load. */
export function StatusPageLogo({ logo }: { logo: string | null | undefined }) {
	const [failed, setFailed] = useState(false)
	useEffect(() => setFailed(false), [logo])
	if (!logo || failed) return null
	return (
		<img
			src={prependBasePath(logo)}
			alt=""
			className="mb-3 h-10 sm:h-12 w-auto max-w-60 object-contain object-left"
			onError={() => setFailed(true)}
		/>
	)
}

/** Footer of a status page: custom text, feed links and "Powered by". */
export function StatusPageFooter({
	slug,
	branding,
	showFeeds,
}: {
	slug: string
	branding?: PublicStatusPageBranding
	showFeeds: boolean
}) {
	return (
		<footer className="pb-6 px-4 grid gap-2 text-center text-xs text-muted-foreground">
			{branding?.footerText && (
				<p className="mx-auto max-w-2xl whitespace-pre-line break-words">{branding.footerText}</p>
			)}
			<div className="flex flex-wrap items-center justify-center gap-x-4 gap-y-1">
				{showFeeds && (
					<>
						<a
							href={statusPageFeedPath(slug, "atom")}
							className="inline-flex items-center gap-1 text-(--sp-accent-muted) hover:underline"
							title={t`Subscribe to incident updates with a feed reader`}
						>
							<RssIcon className="size-3.5" />
							<Trans>Atom feed</Trans>
						</a>
						<a href={statusPageFeedPath(slug, "rss")} className="text-(--sp-accent-muted) hover:underline">
							RSS
						</a>
					</>
				)}
				{!branding?.hidePoweredBy && (
					<span>
						<Trans>
							Powered by{" "}
							<a
								href="https://github.com/AxiomOperator/infrascope"
								target="_blank"
								rel="noopener"
								className="font-medium hover:text-foreground"
							>
								InfraScope
							</a>
						</Trans>
					</span>
				)}
			</div>
		</footer>
	)
}

/** A group of components with its status; collapsible, initially collapsed when set on the page. */
export function ComponentGroup({
	group,
	count,
	children,
}: {
	group: Pick<PublicStatusPageGroup, "name" | "collapsed" | "status">
	count: number
	children: ReactNode
}) {
	const [open, setOpen] = useState(!group.collapsed)
	const contentId = useId()
	const status = group.status || "unknown"
	return (
		<section className="rounded-lg border border-border/60 bg-card shadow-xs" aria-label={group.name}>
			<button
				type="button"
				className="flex w-full items-center gap-2.5 px-4 py-3 text-start rounded-lg hover:bg-muted/40"
				aria-expanded={open}
				aria-controls={contentId}
				onClick={() => setOpen((value) => !value)}
			>
				<ChevronRightIcon
					className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")}
				/>
				<h2 className="font-semibold break-words min-w-0 text-(--sp-accent-text)">{group.name}</h2>
				<span className="text-xs text-muted-foreground tabular-nums">{count}</span>
				<Badge className={cn("ms-auto shrink-0", monitorStatusBadgeColors[status] ?? monitorStatusBadgeColors.unknown)}>
					{monitorStatusLabel(status)}
				</Badge>
			</button>
			{open && (
				<div id={contentId} className="border-t divide-y">
					{children}
				</div>
			)}
		</section>
	)
}
