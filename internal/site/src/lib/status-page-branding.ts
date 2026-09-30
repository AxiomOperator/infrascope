/** Helpers for status page groups, branding, custom domains and badges. */

import type { PublicStatusPage, PublicStatusPageGroup, StatusPageGroup } from "@/types"

/** Same limits as the hub (status_page_branding.go). */
export const MAX_STATUS_PAGE_GROUPS = 50
export const MAX_GROUP_NAME = 100
export const MAX_FOOTER_TEXT = 500
export const MAX_LOGO_SIZE = 512 * 1024
export const MAX_BADGE_LABEL = 40
export const LOGO_TYPES = ["image/png", "image/jpeg", "image/webp", "image/svg+xml"]

export const ACCENT_COLOR_PATTERN = /^#[0-9a-f]{6}$/i
/** Same pattern as the status_pages.customDomain field. */
export const CUSTOM_DOMAIN_PATTERN = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/

/** Normalize a custom domain as the hub does: trimmed, lowercase, without trailing dot. */
export function normalizeCustomDomain(value: string) {
	return value.trim().toLowerCase().replace(/\.$/, "")
}

export function isValidCustomDomain(value: string) {
	const domain = normalizeCustomDomain(value)
	return domain === "" || (domain.length <= 253 && CUSTOM_DOMAIN_PATTERN.test(domain))
}

function parseHex(hex: string): [number, number, number] | null {
	if (!ACCENT_COLOR_PATTERN.test(hex)) return null
	const n = Number.parseInt(hex.slice(1), 16)
	return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

function toHex([r, g, b]: [number, number, number]) {
	return `#${[r, g, b].map((c) => Math.round(c).toString(16).padStart(2, "0")).join("")}`
}

/** WCAG relative luminance of an sRGB color. */
function luminance([r, g, b]: [number, number, number]) {
	const channel = (c: number) => {
		const s = c / 255
		return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
	}
	return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

/** WCAG contrast ratio of two colors (#rrggbb), 1 to 21. */
export function contrastRatio(a: string, b: string) {
	const ca = parseHex(a)
	const cb = parseHex(b)
	if (!ca || !cb) return 1
	const [hi, lo] = [luminance(ca), luminance(cb)].sort((x, y) => y - x)
	return (hi + 0.05) / (lo + 0.05)
}

/** Black or white, whichever is more readable on the color. */
export function readableForeground(background: string) {
	return contrastRatio(background, "#000000") >= contrastRatio(background, "#ffffff") ? "#000000" : "#ffffff"
}

/**
 * The color, mixed towards black (on light backgrounds) or white (on dark
 * ones) until text in it reaches a contrast of minRatio on the background.
 */
export function readableOn(color: string, background: string, minRatio = 4.5) {
	const c = parseHex(color)
	if (!c) return color
	if (contrastRatio(color, background) >= minRatio) return color.toLowerCase()
	const target: [number, number, number] = readableForeground(background) === "#000000" ? [0, 0, 0] : [255, 255, 255]
	for (let step = 1; step <= 20; step++) {
		const t = step / 20
		const mixed = toHex([c[0] + (target[0] - c[0]) * t, c[1] + (target[1] - c[1]) * t, c[2] + (target[2] - c[2]) * t])
		if (contrastRatio(mixed, background) >= minRatio) return mixed
	}
	return toHex(target)
}

/** Background colors of the status page, used for the accent contrast. */
const LIGHT_BACKGROUND = "#ffffff"
const DARK_BACKGROUND = "#161718"

/**
 * CSS custom properties for an accent color: --accent-brand (fills, with
 * --accent-brand-fg on top) and --accent-brand-text (text and links, readable
 * on the page background in the current theme).
 */
export function accentVariables(accentColor: string | undefined, dark: boolean): Record<string, string> | undefined {
	if (!accentColor || !ACCENT_COLOR_PATTERN.test(accentColor)) return undefined
	const color = accentColor.toLowerCase()
	return {
		"--accent-brand": color,
		"--accent-brand-fg": readableForeground(color),
		"--accent-brand-text": readableOn(color, dark ? DARK_BACKGROUND : LIGHT_BACKGROUND),
	}
}

/** Component references of a public group, resolved to page entries. */
export type ResolvedGroupItem =
	| { kind: "system"; index: number; item: PublicStatusPage["systems"][number] }
	| { kind: "monitor"; index: number; item: PublicStatusPage["monitors"][number] }

export interface ResolvedGroup extends Omit<PublicStatusPageGroup, "items"> {
	items: ResolvedGroupItem[]
}

/**
 * Split the components of a public page into its groups and the ungrouped
 * systems and monitors (as indexes, in page order). Out of range references
 * are ignored.
 */
export function layoutComponents(data: Pick<PublicStatusPage, "systems" | "monitors" | "groups">) {
	const systems = data.systems ?? []
	const monitors = data.monitors ?? []
	const groupedSystems = new Set<number>()
	const groupedMonitors = new Set<number>()
	const groups: ResolvedGroup[] = []
	for (const group of data.groups ?? []) {
		const items: ResolvedGroupItem[] = []
		for (const ref of group.items ?? []) {
			if (ref.kind === "system" && systems[ref.index]) {
				items.push({ kind: "system", index: ref.index, item: systems[ref.index] })
				groupedSystems.add(ref.index)
			} else if (ref.kind === "monitor" && monitors[ref.index]) {
				items.push({ kind: "monitor", index: ref.index, item: monitors[ref.index] })
				groupedMonitors.add(ref.index)
			}
		}
		if (items.length > 0) groups.push({ ...group, items })
	}
	return {
		groups,
		systems: systems.map((_, i) => i).filter((i) => !groupedSystems.has(i)),
		monitors: monitors.map((_, i) => i).filter((i) => !groupedMonitors.has(i)),
	}
}

/**
 * Remove components from groups that are no longer on the page, and
 * duplicates, keeping the first occurrence.
 */
export function pruneGroups(groups: StatusPageGroup[], monitors: string[], systems: string[]): StatusPageGroup[] {
	const seen = new Set<string>()
	return groups.map((group) => ({
		...group,
		components: group.components.filter((component) => {
			const key = `${component.type}:${component.id}`
			const onPage = component.type === "monitor" ? monitors.includes(component.id) : systems.includes(component.id)
			if (!onPage || seen.has(key)) return false
			seen.add(key)
			return true
		}),
	}))
}

/** Move an entry of a list by delta positions, returning a new list. */
export function moveItem<T>(list: T[], index: number, delta: number): T[] {
	const target = index + delta
	if (index < 0 || index >= list.length || target < 0 || target >= list.length) return list
	const next = [...list]
	const [item] = next.splice(index, 1)
	next.splice(target, 0, item)
	return next
}

/** Validation message key of groups, or null when they are valid. */
export function groupsProblem(groups: StatusPageGroup[]): "count" | "name" | null {
	if (groups.length > MAX_STATUS_PAGE_GROUPS) return "count"
	if (groups.some((g) => !g.name.trim() || g.name.trim().length > MAX_GROUP_NAME)) return "name"
	return null
}

export type BadgeTarget = { kind: "page" } | { kind: "monitor" | "system"; position: number }

/** The URL of a badge; position is 1-based in page order. */
export function badgeUrl(
	apiBase: string,
	slug: string,
	target: BadgeTarget,
	options: { type?: "status" | "uptime"; period?: "24h" | "7d" | "30d"; label?: string } = {}
) {
	const path =
		target.kind === "page"
			? `${apiBase}/api/beszel/status-pages/${slug}/badge.svg`
			: `${apiBase}/api/beszel/status-pages/${slug}/badge/${target.kind}/${target.position}.svg`
	const params = new URLSearchParams()
	if (options.type && options.type !== "status") params.set("type", options.type)
	if (options.type === "uptime" && options.period) params.set("period", options.period)
	const label = options.label?.trim().slice(0, MAX_BADGE_LABEL)
	if (label) params.set("label", label)
	const query = params.toString()
	return query ? `${path}?${query}` : path
}

function escapeMarkdown(text: string) {
	return text.replace(/[\\[\]()]/g, "\\$&")
}

function escapeHtml(text: string) {
	return text.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`)
}

/** Markdown and HTML snippets that embed a badge linking to the page. */
export function badgeSnippets(imageUrl: string, pageUrl: string, alt: string) {
	return {
		markdown: `[![${escapeMarkdown(alt)}](${imageUrl})](${pageUrl})`,
		html: `<a href="${escapeHtml(pageUrl)}"><img src="${escapeHtml(imageUrl)}" alt="${escapeHtml(alt)}" /></a>`,
	}
}

/**
 * The slug of the status page served on this custom domain, from the meta tag
 * the hub adds to index.html there, or null.
 */
export function getCustomDomainSlug(doc: Pick<Document, "querySelector"> | undefined = globalThis.document) {
	const content = doc?.querySelector?.('meta[name="infrascope-status-slug"]')?.getAttribute("content")
	return content && /^[a-z0-9][a-z0-9-]{1,62}$/.test(content) ? content : null
}
