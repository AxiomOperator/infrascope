import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import {
	ArrowDownIcon,
	ArrowUpIcon,
	ChevronDownIcon,
	CopyIcon,
	ImageIcon,
	PlusIcon,
	Trash2Icon,
	XIcon,
} from "lucide-react"
import { type ReactNode, useEffect, useId, useMemo, useState } from "react"
import { basePath } from "@/components/router"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
	ACCENT_COLOR_PATTERN,
	badgeSnippets,
	badgeUrl,
	contrastRatio,
	isValidCustomDomain,
	LOGO_TYPES,
	MAX_FOOTER_TEXT,
	MAX_GROUP_NAME,
	MAX_LOGO_SIZE,
	MAX_STATUS_PAGE_GROUPS,
	moveItem,
} from "@/lib/status-page-branding"
import { cn, copyToClipboard } from "@/lib/utils"
import type { StatusPageGroup } from "@/types"

/** A section of the status page dialog that can be expanded. */
export function DialogSection({
	title,
	description,
	defaultOpen = false,
	children,
}: {
	title: ReactNode
	description?: ReactNode
	defaultOpen?: boolean
	children: ReactNode
}) {
	const [open, setOpen] = useState(defaultOpen)
	const id = useId()
	return (
		<div className="rounded-md border min-w-0">
			<button
				type="button"
				className="flex w-full items-center justify-between gap-2 px-3 py-2.5 text-start text-sm font-medium hover:bg-muted/40 rounded-md"
				aria-expanded={open}
				aria-controls={id}
				onClick={() => setOpen((value) => !value)}
			>
				<span className="grid gap-0.5">
					<span>{title}</span>
					{description && <span className="text-xs font-normal text-muted-foreground">{description}</span>}
				</span>
				<ChevronDownIcon className={cn("size-4 shrink-0 transition-transform", open && "rotate-180")} />
			</button>
			{open && (
				<div id={id} className="grid gap-4 border-t px-3 py-3 min-w-0">
					{children}
				</div>
			)}
		</div>
	)
}

export interface ComponentOption {
	type: "monitor" | "system"
	id: string
	name: string
}

/** Editor of the component groups of a status page. */
export function StatusPageGroupEditor({
	groups,
	onChange,
	components,
}: {
	groups: StatusPageGroup[]
	onChange: (groups: StatusPageGroup[]) => void
	/** The components of the page, in page order. */
	components: ComponentOption[]
}) {
	const names = useMemo(() => new Map(components.map((c) => [`${c.type}:${c.id}`, c.name])), [components])
	const grouped = new Set(groups.flatMap((g) => g.components.map((c) => `${c.type}:${c.id}`)))
	const available = components.filter((c) => !grouped.has(`${c.type}:${c.id}`))

	const update = (index: number, group: StatusPageGroup) => onChange(groups.map((g, i) => (i === index ? group : g)))

	return (
		<div className="grid gap-3 min-w-0">
			{groups.length === 0 && (
				<p className="text-xs text-muted-foreground">
					<Trans>Without groups, systems and monitors are listed in page order.</Trans>
				</p>
			)}
			{groups.map((group, index) => (
				<div key={index} className="grid gap-2 rounded-md border p-2.5 min-w-0">
					<div className="flex items-center gap-1">
						<Input
							aria-label={t`Group name`}
							placeholder={t`Group name`}
							value={group.name}
							maxLength={MAX_GROUP_NAME}
							required
							className="h-8"
							onChange={(e) => update(index, { ...group, name: e.target.value })}
						/>
						<IconButton label={t`Move up`} disabled={index === 0} onClick={() => onChange(moveItem(groups, index, -1))}>
							<ArrowUpIcon className="size-3.5" />
						</IconButton>
						<IconButton
							label={t`Move down`}
							disabled={index === groups.length - 1}
							onClick={() => onChange(moveItem(groups, index, 1))}
						>
							<ArrowDownIcon className="size-3.5" />
						</IconButton>
						<IconButton label={t`Remove group`} onClick={() => onChange(groups.filter((_, i) => i !== index))}>
							<Trash2Icon className="size-3.5" />
						</IconButton>
					</div>
					{group.components.length > 0 && (
						<ol className="rounded-md border divide-y">
							{group.components.map((component, c) => (
								<li
									key={`${component.type}:${component.id}`}
									className="flex items-center gap-1 ps-2.5 pe-1 py-0.5 min-w-0 text-sm"
								>
									<span className="min-w-0 flex-1 truncate">
										{names.get(`${component.type}:${component.id}`) ?? <Trans>Unavailable</Trans>}
										<span className="ms-2 text-xs text-muted-foreground">
											{component.type === "system" ? <Trans>System</Trans> : <Trans>Monitor</Trans>}
										</span>
									</span>
									<IconButton
										label={t`Move up`}
										disabled={c === 0}
										onClick={() => update(index, { ...group, components: moveItem(group.components, c, -1) })}
									>
										<ArrowUpIcon className="size-3.5" />
									</IconButton>
									<IconButton
										label={t`Move down`}
										disabled={c === group.components.length - 1}
										onClick={() => update(index, { ...group, components: moveItem(group.components, c, 1) })}
									>
										<ArrowDownIcon className="size-3.5" />
									</IconButton>
									<IconButton
										label={t`Remove`}
										onClick={() => update(index, { ...group, components: group.components.filter((_, i) => i !== c) })}
									>
										<XIcon className="size-3.5" />
									</IconButton>
								</li>
							))}
						</ol>
					)}
					<div className="flex flex-wrap items-center gap-3">
						<Select
							value=""
							disabled={available.length === 0}
							onValueChange={(key) => {
								const option = available.find((c) => `${c.type}:${c.id}` === key)
								if (option) {
									update(index, {
										...group,
										components: [...group.components, { type: option.type, id: option.id }],
									})
								}
							}}
						>
							<SelectTrigger className="h-8 flex-1 min-w-40" aria-label={t`Add to group…`}>
								<SelectValue placeholder={available.length === 0 ? t`All components are grouped` : t`Add to group…`} />
							</SelectTrigger>
							<SelectContent>
								{available.map((option) => (
									<SelectItem key={`${option.type}:${option.id}`} value={`${option.type}:${option.id}`}>
										{option.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<div className="flex items-center gap-2 text-xs">
							<Switch
								id={`sp-group-collapsed-${index}`}
								checked={!!group.collapsed}
								onCheckedChange={(collapsed) => update(index, { ...group, collapsed })}
							/>
							<Label htmlFor={`sp-group-collapsed-${index}`} className="text-xs font-normal">
								<Trans>Collapsed</Trans>
							</Label>
						</div>
					</div>
				</div>
			))}
			<Button
				type="button"
				variant="outline"
				size="sm"
				className="justify-self-start"
				disabled={groups.length >= MAX_STATUS_PAGE_GROUPS}
				onClick={() => onChange([...groups, { name: "", collapsed: false, components: [] }])}
			>
				<PlusIcon className="size-4 me-1" />
				<Trans>Add group</Trans>
			</Button>
		</div>
	)
}

function IconButton({
	label,
	disabled,
	onClick,
	children,
}: {
	label: string
	disabled?: boolean
	onClick: () => void
	children: ReactNode
}) {
	return (
		<Button
			type="button"
			variant="ghost"
			size="icon"
			className="size-7 shrink-0"
			disabled={disabled}
			onClick={onClick}
			title={label}
		>
			{children}
			<span className="sr-only">{label}</span>
		</Button>
	)
}

export interface BrandingValues {
	/** A new logo file, null to remove the logo, or undefined to keep it. */
	logoFile: File | null | undefined
	accentColor: string
	footerText: string
	hidePoweredBy: boolean
	customDomain: string
}

/** Logo, accent color, footer and custom domain fields of a status page. */
export function StatusPageBrandingFields({
	values,
	onChange,
	currentLogoUrl,
}: {
	values: BrandingValues
	onChange: (values: BrandingValues) => void
	/** URL of the saved logo, if any. */
	currentLogoUrl?: string
}) {
	const set = (patch: Partial<BrandingValues>) => onChange({ ...values, ...patch })
	const [logoError, setLogoError] = useState("")
	const [preview, setPreview] = useState<string>()
	useEffect(() => {
		if (!values.logoFile) {
			setPreview(undefined)
			return
		}
		const url = URL.createObjectURL(values.logoFile)
		setPreview(url)
		return () => URL.revokeObjectURL(url)
	}, [values.logoFile])
	const logoUrl = values.logoFile === null ? undefined : (preview ?? currentLogoUrl)
	const accentValid = values.accentColor === "" || ACCENT_COLOR_PATTERN.test(values.accentColor)
	const domainValid = isValidCustomDomain(values.customDomain)
	const lowContrast =
		accentValid &&
		values.accentColor !== "" &&
		(contrastRatio(values.accentColor, "#ffffff") < 3 || contrastRatio(values.accentColor, "#161718") < 3)

	return (
		<>
			<div className="grid gap-2">
				<Label htmlFor="sp-logo">
					<Trans>Logo</Trans>
				</Label>
				<div className="flex flex-wrap items-center gap-3">
					<div className="flex h-12 w-24 items-center justify-center rounded-md border bg-muted/30">
						{logoUrl ? (
							<img src={logoUrl} alt="" className="max-h-10 max-w-22 object-contain" />
						) : (
							<ImageIcon className="size-5 text-muted-foreground" />
						)}
					</div>
					<Input
						id="sp-logo"
						type="file"
						accept={LOGO_TYPES.join(",")}
						className="flex-1 min-w-48"
						onChange={(e) => {
							const file = e.target.files?.[0]
							if (!file) return
							if (!LOGO_TYPES.includes(file.type)) {
								setLogoError(t`Use a PNG, JPEG, WebP or SVG image.`)
							} else if (file.size > MAX_LOGO_SIZE) {
								setLogoError(t`The logo can be at most 512 KB.`)
							} else {
								setLogoError("")
								set({ logoFile: file })
								return
							}
							e.target.value = ""
						}}
					/>
					{logoUrl && (
						<Button type="button" variant="ghost" size="sm" onClick={() => set({ logoFile: null })}>
							<Trans>Remove</Trans>
						</Button>
					)}
				</div>
				{logoError ? (
					<p className="text-xs text-destructive">{logoError}</p>
				) : (
					<p className="text-xs text-muted-foreground">
						<Trans>PNG, JPEG, WebP or SVG, up to 512 KB. Shown above the title of public pages.</Trans>
					</p>
				)}
			</div>
			<div className="grid gap-2">
				<Label htmlFor="sp-accent">
					<Trans>Accent color</Trans>
				</Label>
				<div className="flex items-center gap-2">
					<input
						type="color"
						aria-label={t`Pick accent color`}
						className="h-9 w-12 shrink-0 cursor-pointer rounded-md border bg-transparent p-1"
						value={accentValid && values.accentColor ? values.accentColor : "#3b82f6"}
						onChange={(e) => set({ accentColor: e.target.value })}
					/>
					<Input
						id="sp-accent"
						value={values.accentColor}
						placeholder="#3b82f6"
						maxLength={7}
						className="font-mono"
						aria-invalid={!accentValid}
						onChange={(e) => set({ accentColor: e.target.value.trim() })}
					/>
					{values.accentColor && (
						<Button type="button" variant="ghost" size="sm" onClick={() => set({ accentColor: "" })}>
							<Trans>Reset</Trans>
						</Button>
					)}
				</div>
				{!accentValid ? (
					<p className="text-xs text-destructive">
						<Trans>Use a hex color like #3b82f6.</Trans>
					</p>
				) : lowContrast ? (
					<p className="text-xs text-muted-foreground">
						<Trans>Text in this color is adjusted where needed to stay readable in light and dark mode.</Trans>
					</p>
				) : null}
			</div>
			<div className="grid gap-2">
				<Label htmlFor="sp-footer">
					<Trans>Footer text</Trans>
				</Label>
				<Textarea
					id="sp-footer"
					value={values.footerText}
					rows={2}
					maxLength={MAX_FOOTER_TEXT}
					onChange={(e) => set({ footerText: e.target.value })}
				/>
			</div>
			<div className="flex items-center justify-between gap-4">
				<Label htmlFor="sp-hide-powered-by" className="font-medium">
					<Trans>Hide "Powered by InfraScope"</Trans>
				</Label>
				<Switch
					id="sp-hide-powered-by"
					checked={values.hidePoweredBy}
					onCheckedChange={(hidePoweredBy) => set({ hidePoweredBy })}
				/>
			</div>
			<div className="grid gap-2">
				<Label htmlFor="sp-domain">
					<Trans>Custom domain</Trans>
				</Label>
				<Input
					id="sp-domain"
					value={values.customDomain}
					placeholder="status.example.com"
					maxLength={253}
					className="font-mono"
					aria-invalid={!domainValid}
					onChange={(e) => set({ customDomain: e.target.value })}
				/>
				{domainValid ? (
					<p className="text-xs text-muted-foreground">
						<Trans>
							Serve the page at the root of this hostname. Point its DNS to your reverse proxy, which must terminate TLS
							and forward the original Host header to the hub. Only the public page is served there.
						</Trans>
					</p>
				) : (
					<p className="text-xs text-destructive">
						<Trans>Enter a hostname like status.example.com, without https://, port or path.</Trans>
					</p>
				)}
			</div>
		</>
	)
}

/** Copyable badge snippets for a public status page and its components. */
export function StatusPageBadges({
	slug,
	pageUrl,
	systems,
	monitors,
}: {
	slug: string
	pageUrl: string
	/** Names of the page's systems and monitors, in page order. */
	systems: string[]
	monitors: string[]
}) {
	const [type, setType] = useState<"status" | "uptime">("status")
	const [period, setPeriod] = useState<"24h" | "7d" | "30d">("30d")
	const apiBase = `${window.location.origin}${basePath}`.replace(/\/+$/, "")
	const rows: { key: string; name: string; url: string }[] = [
		{ key: "page", name: t`Overall`, url: badgeUrl(apiBase, slug, { kind: "page" }, { type, period }) },
		...systems.map((name, i) => ({
			key: `system-${i}`,
			name,
			url: badgeUrl(apiBase, slug, { kind: "system", position: i + 1 }, { type, period }),
		})),
		...monitors.map((name, i) => ({
			key: `monitor-${i}`,
			name,
			url: badgeUrl(apiBase, slug, { kind: "monitor", position: i + 1 }, { type, period }),
		})),
	]
	return (
		<div className="grid gap-3 min-w-0">
			<div className="flex flex-wrap gap-2">
				<Select value={type} onValueChange={(value) => setType(value as typeof type)}>
					<SelectTrigger className="h-8 w-36" aria-label={t`Badge type`}>
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="status">
							<Trans>Status</Trans>
						</SelectItem>
						<SelectItem value="uptime">
							<Trans>Uptime</Trans>
						</SelectItem>
					</SelectContent>
				</Select>
				{type === "uptime" && (
					<Select value={period} onValueChange={(value) => setPeriod(value as typeof period)}>
						<SelectTrigger className="h-8 w-28" aria-label={t`Period`}>
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="24h">{t`24h`}</SelectItem>
							<SelectItem value="7d">{t`7d`}</SelectItem>
							<SelectItem value="30d">{t`30d`}</SelectItem>
						</SelectContent>
					</Select>
				)}
			</div>
			<ul className="rounded-md border divide-y">
				{rows.map((row) => {
					const snippets = badgeSnippets(row.url, pageUrl, row.name)
					return (
						<li key={row.key} className="flex flex-wrap items-center gap-2 px-2.5 py-2 min-w-0">
							<span className="min-w-0 flex-1 truncate text-sm">{row.name}</span>
							{/* previews count against the public rate limit, so only the overall badge has one */}
							{row.key === "page" && <img src={row.url} alt="" className="h-5" />}
							<Button
								type="button"
								variant="outline"
								size="sm"
								className="h-7 px-2 text-xs"
								onClick={() => copyToClipboard(snippets.markdown)}
							>
								<CopyIcon className="size-3 me-1" />
								Markdown
							</Button>
							<Button
								type="button"
								variant="outline"
								size="sm"
								className="h-7 px-2 text-xs"
								onClick={() => copyToClipboard(snippets.html)}
							>
								<CopyIcon className="size-3 me-1" />
								HTML
							</Button>
						</li>
					)
				})}
			</ul>
		</div>
	)
}
