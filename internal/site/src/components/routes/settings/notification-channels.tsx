import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { InfoIcon, LoaderCircleIcon, PencilIcon, PlusIcon, SendIcon, SettingsIcon, Trash2Icon } from "lucide-react"
import type { ClientResponseError } from "pocketbase"
import { type ComponentType, type FormEvent, lazy, Suspense, useMemo, useState } from "react"
import {
	ChannelTypeIcon,
	channelTypeLabel,
	SeverityBadge,
	SeveritySelect,
	severityLabel,
	useNotificationChannels,
} from "@/components/notification-channels"
import { prependBasePath } from "@/components/router"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { InputTags } from "@/components/ui/input-tags"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { toast } from "@/components/ui/use-toast"
import { isAdmin, isReadOnlyUser, pb } from "@/lib/api"
import {
	CHANNEL_NAME_MAX_CHARS,
	type ChannelDraft,
	channelPayload,
	draftFromChannel,
	hasLegacyDestinations,
	legacyChannelDrafts,
	newChannelDraft,
	validateChannelDraft,
} from "@/lib/notification-channels"
import type { NotificationChannelRecord, NotificationChannelType, UserSettings } from "@/types"
import { describeUrl, NotificationDialog } from "./notification-builder"
import { TemplateEditor } from "./notification-templates"

const collection = () => pb.collection<NotificationChannelRecord>("notification_channels")

const errorMessage = (e: unknown) =>
	(e as ClientResponseError)?.response?.message || (e as Error)?.message || t`Check logs for more details.`

// The browser push subscription UI is provided by the web push feature when present.
const pushModules = import.meta.glob<Record<string, unknown>>([
	"/src/components/push-notifications.tsx",
	"/src/components/**/web-push*.tsx",
	"/src/components/**/push-subscription*.tsx",
	"/src/components/**/browser-push*.tsx",
])
const pushLoader = Object.values(pushModules)[0]
const BrowserPushStatus = pushLoader
	? lazy(async () => {
			const mod = await pushLoader()
			const component =
				mod.default ??
				Object.entries(mod).find(([name, value]) => /Push/.test(name) && typeof value === "function")?.[1]
			return { default: (component ?? (() => null)) as ComponentType }
		})
	: null

/** Sends a test notification through a saved channel. */
async function testChannel(channel: NotificationChannelRecord) {
	try {
		const res = await pb.send<{ err: string | false }>(`/api/beszel/notification-channels/${channel.id}/test`, {
			method: "POST",
		})
		if (!res.err) {
			toast({ title: t`Test notification sent`, description: t`Check your notification service` })
			return
		}
		toast({ title: t`Error`, description: res.err, variant: "destructive" })
	} catch (e) {
		toast({ title: t`Error`, description: errorMessage(e), variant: "destructive" })
	}
}

function channelSummary(channel: NotificationChannelRecord) {
	switch (channel.type) {
		case "email":
			return (channel.config?.addresses ?? []).join(", ")
		case "shoutrrr": {
			const url = channel.config?.url
			if (!url) return ""
			const info = describeUrl(url)
			return info.summary ? `${info.name} · ${info.summary}` : info.name
		}
		default:
			return t`Devices subscribed to browser notifications`
	}
}

function ChannelCard({
	channel,
	readOnly,
	onEdit,
}: {
	channel: NotificationChannelRecord
	readOnly: boolean
	onEdit: () => void
}) {
	const [testing, setTesting] = useState(false)
	const [toggling, setToggling] = useState(false)

	async function setEnabled(enabled: boolean) {
		setToggling(true)
		try {
			await collection().update(channel.id, { enabled })
		} catch (e) {
			toast({ title: t`Failed to update channel`, description: errorMessage(e), variant: "destructive" })
		}
		setToggling(false)
	}

	async function remove() {
		if (
			!window.confirm(t`Delete the channel "${channel.name}"? Alerts sent only to it will use the default channels.`)
		) {
			return
		}
		try {
			await collection().delete(channel.id)
		} catch (e) {
			toast({ title: t`Failed to delete channel`, description: errorMessage(e), variant: "destructive" })
		}
	}

	return (
		<Card className="bg-table-header p-2.5 md:p-3">
			<div className="flex flex-wrap items-center gap-3 sm:flex-nowrap">
				<span
					className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted light:bg-card text-muted-foreground"
					title={channelTypeLabel(channel.type)}
				>
					<ChannelTypeIcon type={channel.type} />
				</span>
				<div className="min-w-0 flex-1">
					<div className="flex min-w-0 flex-wrap items-center gap-1.5 text-sm">
						<span className="font-medium truncate">{channel.name}</span>
						{channel.isDefault && (
							<Badge variant="outline" className="px-1.5 py-0 text-[0.7rem] font-medium">
								<Trans>Default</Trans>
							</Badge>
						)}
						<SeverityBadge severity={channel.minSeverity} />
						{channel.minSeverity !== "critical" && (
							<span className="text-xs text-muted-foreground">
								<Trans context="Severity threshold, e.g. Warning and above">and above</Trans>
							</span>
						)}
					</div>
					<p className="truncate text-xs text-muted-foreground">{channelSummary(channel)}</p>
				</div>
				<div className="flex shrink-0 items-center gap-1 max-sm:w-full max-sm:justify-end">
					<Switch
						checked={channel.enabled}
						disabled={readOnly || toggling}
						onCheckedChange={setEnabled}
						aria-label={t`Enable ${channel.name}`}
						className="me-2"
					/>
					<Button
						type="button"
						variant="outline"
						size="sm"
						className="h-9"
						disabled={testing || readOnly}
						onClick={async () => {
							setTesting(true)
							await testChannel(channel)
							setTesting(false)
						}}
					>
						{testing ? <LoaderCircleIcon className="size-4 animate-spin" /> : <SendIcon className="size-4" />}
						<span className="ms-1">
							<Trans>Test</Trans>
						</span>
					</Button>
					<Button
						type="button"
						variant="outline"
						size="icon"
						className="size-9"
						aria-label={t`Edit ${channel.name}`}
						title={t`Edit`}
						disabled={readOnly}
						onClick={onEdit}
					>
						<PencilIcon className="size-4" />
					</Button>
					<Button
						type="button"
						variant="outline"
						size="icon"
						className="size-9"
						aria-label={t`Delete ${channel.name}`}
						title={t`Delete`}
						disabled={readOnly}
						onClick={remove}
					>
						<Trash2Icon className="size-4" />
					</Button>
				</div>
			</div>
		</Card>
	)
}

const CHANNEL_TYPES: NotificationChannelType[] = ["email", "shoutrrr", "browser"]

function ChannelDialog({
	channel,
	onClose,
}: {
	/** channel being edited, null when adding */
	channel: NotificationChannelRecord | null
	onClose: () => void
}) {
	const [draft, setDraft] = useState<ChannelDraft>(() =>
		channel ? draftFromChannel(channel) : newChannelDraft("email", channelTypeLabel("email"))
	)
	const [showErrors, setShowErrors] = useState(false)
	const [saving, setSaving] = useState(false)
	const [editingUrl, setEditingUrl] = useState(false)
	const [showTemplate, setShowTemplate] = useState(!!draft.template)
	const errors = validateChannelDraft(draft)
	const update = (patch: Partial<ChannelDraft>) => setDraft((d) => ({ ...d, ...patch }))
	const urlInfo = useMemo(() => (draft.config.url ? describeUrl(draft.config.url) : null), [draft.config.url])

	function changeType(type: NotificationChannelType) {
		const fresh = newChannelDraft(type)
		// keep a name the user typed; replace the default name of the previous type
		const typedName = draft.name.trim() && draft.name !== channelTypeLabel(draft.type)
		setDraft({ ...draft, type, config: fresh.config, name: typedName ? draft.name : channelTypeLabel(type) })
		setShowErrors(false)
	}

	async function save(e: FormEvent) {
		e.preventDefault()
		if (Object.keys(errors).length) {
			setShowErrors(true)
			return
		}
		setSaving(true)
		try {
			const payload = channelPayload(draft)
			if (channel) {
				await collection().update(channel.id, payload)
			} else {
				await collection().create({ ...payload, user: pb.authStore.record?.id })
			}
			onClose()
		} catch (e) {
			toast({ title: t`Failed to save channel`, description: errorMessage(e), variant: "destructive" })
		}
		setSaving(false)
	}

	return (
		<DialogContent className="max-h-[90vh] overflow-auto sm:max-w-xl">
			<form onSubmit={save} className="grid gap-4" noValidate>
				<DialogHeader>
					<DialogTitle>{channel ? <Trans>Edit channel</Trans> : <Trans>Add channel</Trans>}</DialogTitle>
					<DialogDescription>
						<Trans>
							Default channels receive every alert at or above their minimum severity, unless an alert is sent to
							specific channels.
						</Trans>
					</DialogDescription>
				</DialogHeader>
				<div className="grid sm:grid-cols-2 gap-3">
					<div className="grid gap-1.5">
						<Label htmlFor="ch-type">
							<Trans>Type</Trans>
						</Label>
						<Select
							value={draft.type}
							onValueChange={(v) => changeType(v as NotificationChannelType)}
							disabled={!!channel}
						>
							<SelectTrigger id="ch-type">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{CHANNEL_TYPES.map((type) => (
									<SelectItem key={type} value={type}>
										<span className="flex items-center gap-2">
											<ChannelTypeIcon type={type} className="size-3.5" />
											{channelTypeLabel(type)}
										</span>
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					<div className="grid gap-1.5">
						<Label htmlFor="ch-name">
							<Trans>Name</Trans>
						</Label>
						<Input
							id="ch-name"
							value={draft.name}
							maxLength={CHANNEL_NAME_MAX_CHARS}
							aria-invalid={showErrors && !!errors.name}
							onChange={(e) => update({ name: e.target.value })}
						/>
					</div>
				</div>

				{draft.type === "email" && (
					<div className="grid gap-1.5">
						<Label htmlFor="ch-email">
							<Trans>To email(s)</Trans>
						</Label>
						<InputTags
							id="ch-email"
							type="email"
							value={draft.config.addresses ?? []}
							onChange={(next: string[] | ((prev: string[]) => string[])) =>
								setDraft((d) => ({
									...d,
									config: {
										...d.config,
										addresses: typeof next === "function" ? next(d.config.addresses ?? []) : next,
									},
								}))
							}
							placeholder={t`Enter email address...`}
							className="w-full"
						/>
						<p className={showErrors && errors.config ? "text-xs text-destructive" : "text-xs text-muted-foreground"}>
							<Trans>Save address using enter key or comma. Add at least one valid address.</Trans>
						</p>
						{isAdmin() && (
							<p className="text-xs text-muted-foreground">
								<Trans>
									Please{" "}
									<a href={prependBasePath("/_/#/settings/mail")} className="link" target="_blank">
										configure an SMTP server
									</a>{" "}
									to ensure alerts are delivered.
								</Trans>
							</p>
						)}
					</div>
				)}

				{draft.type === "shoutrrr" && (
					<div className="grid gap-1.5">
						<span className="text-sm font-medium">
							<Trans>Notification service</Trans>
						</span>
						<div className="flex items-center gap-2 rounded-md border p-2.5">
							<div className="min-w-0 flex-1">
								{urlInfo ? (
									<>
										<p className="text-sm font-medium">
											{urlInfo.name}
											{urlInfo.summary && (
												<span className="font-normal text-muted-foreground"> · {urlInfo.summary}</span>
											)}
										</p>
										<p className="truncate font-mono text-xs text-muted-foreground" title={urlInfo.masked}>
											{urlInfo.masked}
										</p>
									</>
								) : (
									<p
										className={
											showErrors && errors.config ? "text-sm text-destructive" : "text-sm text-muted-foreground"
										}
									>
										<Trans>No service configured.</Trans>
									</p>
								)}
							</div>
							<Button type="button" variant="outline" size="sm" onClick={() => setEditingUrl(true)}>
								<SettingsIcon className="size-4" />
								<span className="ms-1">{urlInfo ? <Trans>Change</Trans> : <Trans>Configure</Trans>}</span>
							</Button>
						</div>
						<p className="text-xs text-muted-foreground">
							<Trans>
								InfraScope uses{" "}
								<a href="https://beszel.dev/guide/notifications" target="_blank" className="link" rel="noopener">
									Shoutrrr
								</a>{" "}
								to integrate with popular notification services. The URL is stored encrypted.
							</Trans>
						</p>
						<Dialog open={editingUrl} onOpenChange={setEditingUrl}>
							{editingUrl && (
								<NotificationDialog
									url={draft.config.url || null}
									onSubmit={(url) => {
										setDraft((d) => {
											const autoName =
												!d.name.trim() || d.name === channelTypeLabel("shoutrrr") || d.name === urlInfo?.name
											return {
												...d,
												config: { ...d.config, url },
												name: autoName ? describeUrl(url).name.slice(0, CHANNEL_NAME_MAX_CHARS) : d.name,
											}
										})
										setEditingUrl(false)
									}}
									onCancel={() => setEditingUrl(false)}
								/>
							)}
						</Dialog>
					</div>
				)}

				{draft.type === "browser" && (
					<div className="grid gap-1.5 rounded-md border p-3">
						<p className="text-sm text-muted-foreground">
							<Trans>Sends push notifications to the browsers and devices where you enabled notifications.</Trans>
						</p>
						{BrowserPushStatus ? (
							<Suspense fallback={<LoaderCircleIcon className="size-4 animate-spin" />}>
								<BrowserPushStatus />
							</Suspense>
						) : (
							<p className="text-xs text-muted-foreground">
								<Trans>Browser notifications are not available in this version.</Trans>
							</p>
						)}
					</div>
				)}

				<div className="grid sm:grid-cols-2 gap-3 items-end">
					<div className="grid gap-1.5">
						<Label htmlFor="ch-severity">
							<Trans>Minimum severity</Trans>
						</Label>
						<SeveritySelect
							id="ch-severity"
							value={draft.minSeverity}
							onChange={(v) => update({ minSeverity: v || "info" })}
						/>
					</div>
					<label htmlFor="ch-default" className="flex items-center justify-between gap-3 rounded-md border px-3 h-10">
						<span className="text-sm">
							<Trans>Default channel</Trans>
						</span>
						<Switch id="ch-default" checked={draft.isDefault} onCheckedChange={(v) => update({ isDefault: v })} />
					</label>
				</div>
				<p className="-mt-2 text-xs text-muted-foreground">
					{draft.isDefault ? (
						<Trans>
							Receives alerts of severity {severityLabel(draft.minSeverity)} and above, and alerts sent to it.
						</Trans>
					) : (
						<Trans>Only receives alerts that are sent to it explicitly.</Trans>
					)}
				</p>
				<label htmlFor="ch-enabled" className="flex items-center justify-between gap-3">
					<span className="text-sm">
						<Trans>Enabled</Trans>
					</span>
					<Switch id="ch-enabled" checked={draft.enabled} onCheckedChange={(v) => update({ enabled: v })} />
				</label>

				<div className="grid gap-3">
					<label htmlFor="ch-template" className="flex items-center justify-between gap-3">
						<span className="text-sm">
							<Trans>Custom message template</Trans>
						</span>
						<Switch
							id="ch-template"
							checked={showTemplate}
							onCheckedChange={(v) => {
								setShowTemplate(v)
								if (!v) update({ template: null })
							}}
						/>
					</label>
					{showTemplate && (
						<TemplateEditor
							idPrefix="ch-tpl"
							value={draft.template ?? {}}
							onChange={(template) => update({ template })}
						/>
					)}
				</div>

				<DialogFooter className="gap-2 sm:gap-2">
					<Button type="button" variant="ghost" onClick={onClose}>
						<Trans>Cancel</Trans>
					</Button>
					<Button type="submit" disabled={saving}>
						{saving && <LoaderCircleIcon className="size-4 animate-spin me-1" />}
						{channel ? <Trans>Update</Trans> : <Trans>Add</Trans>}
					</Button>
				</DialogFooter>
			</form>
		</DialogContent>
	)
}

/** Settings section listing the user's notification channels. */
export function NotificationChannels({ userSettings }: { userSettings: UserSettings }) {
	const { channels, loading } = useNotificationChannels()
	const readOnly = isReadOnlyUser()
	// channel being edited, "new" when adding, null when the dialog is closed
	const [editing, setEditing] = useState<NotificationChannelRecord | "new" | null>(null)
	const [dialogKey, setDialogKey] = useState(0)
	const [converting, setConverting] = useState(false)
	const showLegacy = !loading && channels.length === 0 && hasLegacyDestinations(userSettings)

	function open(target: NotificationChannelRecord | "new") {
		setDialogKey((k) => k + 1)
		setEditing(target)
	}

	async function convertLegacy() {
		setConverting(true)
		try {
			const drafts = legacyChannelDrafts(userSettings, (url) => describeUrl(url).name)
			for (const draft of drafts) {
				await collection().create({ ...channelPayload(draft), user: pb.authStore.record?.id })
			}
		} catch (e) {
			toast({ title: t`Failed to create channels`, description: errorMessage(e), variant: "destructive" })
		}
		setConverting(false)
	}

	return (
		<div className="space-y-3">
			<div className="grid grid-cols-1 sm:flex items-center justify-between gap-4">
				<div>
					<h3 className="mb-1 text-lg font-medium">
						<Trans>Channels</Trans>
					</h3>
					<p className="text-sm text-muted-foreground leading-relaxed">
						<Trans>
							Where alerts are sent: email, webhook and push services, or browser notifications. Critical alerts include
							down systems and monitors; threshold alerts are warnings.
						</Trans>
					</p>
				</div>
				<Button
					type="button"
					variant="outline"
					className="h-10 shrink-0"
					disabled={readOnly}
					onClick={() => open("new")}
				>
					<PlusIcon className="size-4" />
					<span className="ms-1">
						<Trans>Add channel</Trans>
					</span>
				</Button>
			</div>
			{showLegacy && (
				<div className="flex flex-wrap items-start gap-3 rounded-md border border-dashed p-3 text-sm">
					<InfoIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
					<div className="min-w-0 flex-1 text-muted-foreground">
						<p>
							<Trans>You have no channels yet, so alerts are sent to your previous notification settings:</Trans>
						</p>
						<ul className="mt-1 list-disc ps-5">
							{(userSettings.emails ?? []).filter(Boolean).map((email) => (
								<li key={email} className="truncate">
									{email}
								</li>
							))}
							{(userSettings.webhooks ?? []).filter(Boolean).map((url, i) => (
								<li key={`${i}-${url}`} className="truncate font-mono text-xs">
									{describeUrl(url).masked}
								</li>
							))}
						</ul>
					</div>
					<Button type="button" size="sm" disabled={converting || readOnly} onClick={convertLegacy}>
						{converting && <LoaderCircleIcon className="size-4 animate-spin me-1" />}
						<Trans>Create channels from these</Trans>
					</Button>
				</div>
			)}
			{loading ? (
				<div className="flex justify-center p-4">
					<LoaderCircleIcon className="size-5 animate-spin text-muted-foreground" />
				</div>
			) : channels.length > 0 ? (
				<div className="grid gap-2.5">
					{channels.map((channel) => (
						<ChannelCard key={channel.id} channel={channel} readOnly={readOnly} onEdit={() => open(channel)} />
					))}
				</div>
			) : (
				!showLegacy && (
					<p className="rounded-md border border-dashed p-4 text-center text-sm text-muted-foreground">
						<Trans>No channels configured. Alerts are not sent anywhere.</Trans>
					</p>
				)
			)}
			<Dialog open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
				{editing !== null && (
					<ChannelDialog
						key={dialogKey}
						channel={editing === "new" ? null : editing}
						onClose={() => setEditing(null)}
					/>
				)}
			</Dialog>
		</div>
	)
}
