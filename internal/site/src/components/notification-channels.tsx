import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { BellRingIcon, ChevronDownIcon, MailIcon, MonitorSmartphoneIcon, WebhookIcon } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
	DropdownMenu,
	DropdownMenuCheckboxItem,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { SEVERITIES, sortChannels } from "@/lib/notification-channels"
import { useOwnedRecords } from "@/lib/use-owned-records"
import { cn } from "@/lib/utils"
import type { AlertSeverity, NotificationChannelRecord, NotificationChannelType } from "@/types"

/** The user's notification channels, kept in sync in realtime and sorted defaults first. */
export function useNotificationChannels() {
	const { records, loading } = useOwnedRecords<NotificationChannelRecord>("notification_channels", "name")
	return { channels: sortChannels(records), loading }
}

export function severityLabel(severity: AlertSeverity) {
	switch (severity) {
		case "critical":
			return t`Critical`
		case "warning":
			return t`Warning`
		default:
			return t`Info`
	}
}

export function channelTypeLabel(type: NotificationChannelType) {
	switch (type) {
		case "email":
			return t`Email`
		case "shoutrrr":
			return t`Webhook / Push`
		default:
			return t`Browser push`
	}
}

export function ChannelTypeIcon({ type, className }: { type: NotificationChannelType; className?: string }) {
	const Icon = type === "email" ? MailIcon : type === "shoutrrr" ? WebhookIcon : MonitorSmartphoneIcon
	return <Icon className={cn("size-4", className)} aria-hidden="true" />
}

const severityVariants = {
	info: "secondary",
	warning: "warning",
	critical: "danger",
} as const

export function SeverityBadge({ severity, className }: { severity: AlertSeverity; className?: string }) {
	return (
		<Badge variant={severityVariants[severity]} className={cn("px-1.5 py-0 text-[0.7rem] font-medium", className)}>
			{severityLabel(severity)}
		</Badge>
	)
}

const DEFAULT_SEVERITY = "__default__"

/**
 * Severity select. With allowDefault, the empty value means the alert type's default severity.
 */
export function SeveritySelect({
	id,
	value,
	onChange,
	allowDefault = false,
	disabled,
	className,
}: {
	id?: string
	value: AlertSeverity | ""
	onChange: (value: AlertSeverity | "") => void
	allowDefault?: boolean
	disabled?: boolean
	className?: string
}) {
	return (
		<Select
			value={value || (allowDefault ? DEFAULT_SEVERITY : "info")}
			onValueChange={(v) => onChange(v === DEFAULT_SEVERITY ? "" : (v as AlertSeverity))}
			disabled={disabled}
		>
			<SelectTrigger id={id} className={className}>
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				{allowDefault && (
					<SelectItem value={DEFAULT_SEVERITY}>
						<Trans context="Default severity of an alert type">Default</Trans>
					</SelectItem>
				)}
				{SEVERITIES.map((severity) => (
					<SelectItem key={severity} value={severity}>
						{severityLabel(severity)}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	)
}

/**
 * Multi-select of the channels an alert is sent to. Empty means default routing ("Default channels").
 * Ids of channels not in the list (e.g. other users' channels on a shared monitor) are kept.
 */
export function ChannelMultiSelect({
	id,
	value,
	onChange,
	channels,
	disabled,
	className,
}: {
	id?: string
	value: string[]
	onChange: (ids: string[]) => void
	channels: NotificationChannelRecord[]
	disabled?: boolean
	className?: string
}) {
	const known = value.filter((channelId) => channels.some((c) => c.id === channelId))
	const selected = new Set(known)
	const label =
		known.length === 0
			? t`Default channels`
			: known.length === 1
				? (channels.find((c) => c.id === known[0])?.name ?? "")
				: t`${known.length} channels`
	const others = value.filter((channelId) => !channels.some((c) => c.id === channelId))
	const toggle = (channelId: string, checked: boolean) => {
		const next = new Set(known)
		if (checked) next.add(channelId)
		else next.delete(channelId)
		onChange([...others, ...channels.filter((c) => next.has(c.id)).map((c) => c.id)])
	}
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					id={id}
					type="button"
					variant="outline"
					disabled={disabled}
					className={cn("relative w-full min-w-0 ps-9 pe-9 justify-start font-normal text-start", className)}
				>
					<BellRingIcon className="size-3.5 absolute start-3.5 top-1/2 -translate-y-1/2 opacity-85" />
					<span className="truncate">{label}</span>
					<ChevronDownIcon className="size-4 absolute end-3.5 top-1/2 -translate-y-1/2 opacity-50" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				align="start"
				className="w-[var(--radix-dropdown-menu-trigger-width)] max-h-80 overflow-auto"
			>
				<DropdownMenuCheckboxItem
					checked={known.length === 0}
					onSelect={(e) => e.preventDefault()}
					onCheckedChange={() => onChange(others)}
				>
					<Trans>Default channels</Trans>
				</DropdownMenuCheckboxItem>
				{channels.length > 0 && <DropdownMenuSeparator />}
				{channels.map((channel) => (
					<DropdownMenuCheckboxItem
						key={channel.id}
						checked={selected.has(channel.id)}
						onSelect={(e) => e.preventDefault()}
						onCheckedChange={(checked) => toggle(channel.id, checked === true)}
						className="gap-2"
					>
						<ChannelTypeIcon type={channel.type} className="size-3.5 opacity-70" />
						<span className={cn("truncate", !channel.enabled && "text-muted-foreground line-through")}>
							{channel.name}
						</span>
					</DropdownMenuCheckboxItem>
				))}
				{channels.length === 0 && (
					<DropdownMenuItem disabled>
						<Trans>No channels yet</Trans>
					</DropdownMenuItem>
				)}
			</DropdownMenuContent>
		</DropdownMenu>
	)
}

/** Severity and destination controls of an alert, laid out side by side. */
export function AlertRoutingFields({
	idPrefix,
	severity,
	onSeverityChange,
	channelIds,
	onChannelsChange,
	channels,
	disabled,
	defaultHint,
}: {
	idPrefix: string
	severity: AlertSeverity | ""
	onSeverityChange: (value: AlertSeverity | "") => void
	channelIds: string[]
	onChannelsChange: (ids: string[]) => void
	channels: NotificationChannelRecord[]
	disabled?: boolean
	/** default severity of the alert type, shown next to the "Default" option */
	defaultHint?: AlertSeverity
}) {
	return (
		<div className="grid sm:grid-cols-2 gap-3">
			<div className="grid gap-1.5">
				<label htmlFor={`${idPrefix}-severity`} className="text-sm text-muted-foreground">
					<Trans>Severity</Trans>
					{defaultHint && !severity && (
						<span className="opacity-80">
							{" "}
							({t`default`}: {severityLabel(defaultHint)})
						</span>
					)}
				</label>
				<SeveritySelect
					id={`${idPrefix}-severity`}
					value={severity}
					onChange={onSeverityChange}
					allowDefault
					disabled={disabled}
					className="h-9"
				/>
			</div>
			<div className="grid gap-1.5">
				<label htmlFor={`${idPrefix}-channels`} className="text-sm text-muted-foreground">
					<Trans>Send to</Trans>
				</label>
				<ChannelMultiSelect
					id={`${idPrefix}-channels`}
					value={channelIds}
					onChange={onChannelsChange}
					channels={channels}
					disabled={disabled}
					className="h-9"
				/>
			</div>
		</div>
	)
}
