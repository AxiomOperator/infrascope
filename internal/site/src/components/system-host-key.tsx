import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { CircleAlertIcon, KeyRoundIcon } from "lucide-react"
import { useState } from "react"
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { toast } from "@/components/ui/use-toast"
import { isReadOnlyUser, pb } from "@/lib/api"
import { ConnectionType, SystemStatus } from "@/lib/enums"
import { cn } from "@/lib/utils"
import type { SystemRecord } from "@/types"

/** Text of the hub's ErrHostKeyMismatch (internal/hub/systems/hostkey.go). */
const HOST_KEY_MISMATCH = "ssh host key mismatch"

/** Whether a system's down reason is a pinned SSH host key mismatch. */
export function isHostKeyMismatch(reason?: string) {
	return !!reason && reason.toLowerCase().includes(HOST_KEY_MISMATCH)
}

/** Down reason of a system, or "" when it's not down or has no reason. */
export function getDownReason(system: Pick<SystemRecord, "status" | "downReason">) {
	return system.status === SystemStatus.Down ? (system.downReason ?? "") : ""
}

/**
 * Whether the current user may reset the system's pinned SSH host key. Hidden for
 * read-only users and for systems connected over WebSocket (which don't use SSH),
 * unless the system is down with a host key mismatch.
 */
export function canResetHostKey(system: SystemRecord) {
	if (isReadOnlyUser()) {
		return false
	}
	return system.info?.ct !== ConnectionType.WebSocket || isHostKeyMismatch(system.downReason)
}

/** Clears the SSH host key pinned for a system, so the next key the agent presents is trusted. */
export async function resetHostKey(system: Pick<SystemRecord, "id" | "name">) {
	try {
		await pb.send(`/api/beszel/systems/${encodeURIComponent(system.id)}/reset-host-key`, { method: "POST" })
		toast({
			title: t`Host key reset`,
			description: t`The next SSH host key presented by ${system.name} will be trusted.`,
		})
	} catch (error) {
		const message = (error as { response?: { message?: string } })?.response?.message || (error as Error)?.message
		toast({
			variant: "destructive",
			title: t`Failed to reset host key`,
			description: message || t`Please check logs for more details.`,
		})
	}
}

/** Confirmation dialog for resetting a system's pinned SSH host key. */
export function ResetHostKeyDialog({
	system,
	open,
	onOpenChange,
}: {
	system: Pick<SystemRecord, "id" | "name">
	open: boolean
	onOpenChange: (open: boolean) => void
}) {
	const { name } = system
	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>
						<Trans>Reset SSH host key for {name}?</Trans>
					</AlertDialogTitle>
					<AlertDialogDescription>
						<Trans>
							The hub pins the SSH host key of each agent and refuses to connect if it changes. Reset it only if you
							reinstalled the agent or otherwise changed its host key. The next key the agent presents will be trusted.
						</Trans>
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel>
						<Trans>Cancel</Trans>
					</AlertDialogCancel>
					<AlertDialogAction onClick={() => resetHostKey(system)}>
						<Trans>Reset host key</Trans>
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	)
}

/** Why a down system is down, with a host key reset action for host key mismatches. */
export function SystemDownReason({ system, className }: { system: SystemRecord; className?: string }) {
	const [open, setOpen] = useState(false)
	const reason = getDownReason(system)
	if (!reason) {
		return null
	}
	const showReset = isHostKeyMismatch(reason) && canResetHostKey(system)
	return (
		<div className={cn("flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground", className)}>
			<CircleAlertIcon className="size-4 shrink-0 text-red-500" />
			<span className="break-words min-w-0">
				<Trans>Down: {reason}</Trans>
			</span>
			{showReset && (
				<>
					<Button variant="outline" size="sm" className="h-7 px-2" onClick={() => setOpen(true)}>
						<KeyRoundIcon className="size-3.5 me-1.5" />
						<Trans>Reset host key</Trans>
					</Button>
					<ResetHostKeyDialog system={system} open={open} onOpenChange={setOpen} />
				</>
			)}
		</div>
	)
}
