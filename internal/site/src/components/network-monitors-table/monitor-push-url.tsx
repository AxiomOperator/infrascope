import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { CopyIcon, RefreshCwIcon } from "lucide-react"
import { useEffect, useState } from "react"
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useToast } from "@/components/ui/use-toast"
import { isReadOnlyUser, pb } from "@/lib/api"
import { getPushUrl } from "@/lib/network-monitor-utils"
import { cn, copyToClipboard } from "@/lib/utils"
import { getErrorMessage } from "./monitor-form-utils"

/** Push URL of a push monitor with copy and regenerate actions. */
export function MonitorPushUrl({
	monitorId,
	pushToken,
	className,
}: {
	monitorId: string
	pushToken?: string
	className?: string
}) {
	const [token, setToken] = useState(pushToken ?? "")
	const [regenerating, setRegenerating] = useState(false)
	const { toast } = useToast()
	const inputId = `push-url-${monitorId}`

	// follow realtime updates of the record
	useEffect(() => {
		if (pushToken) setToken(pushToken)
	}, [pushToken])

	if (!token) {
		return null
	}

	const url = getPushUrl(token)

	async function regenerate() {
		setRegenerating(true)
		try {
			const res = await pb.send<{ pushToken: string }>(`/api/beszel/monitors/${monitorId}/push-token`, {
				method: "POST",
			})
			if (res?.pushToken) setToken(res.pushToken)
		} catch (err) {
			toast({ variant: "destructive", title: t`Error`, description: getErrorMessage(err) })
		} finally {
			setRegenerating(false)
		}
	}

	return (
		<div className={cn("grid gap-2", className)}>
			<Label htmlFor={inputId}>
				<Trans>Push URL</Trans>
			</Label>
			<div className="flex gap-2">
				<Input
					id={inputId}
					readOnly
					value={url}
					className="font-mono text-xs"
					onFocus={(event) => event.currentTarget.select()}
				/>
				<Button
					type="button"
					variant="outline"
					size="icon"
					className="shrink-0"
					aria-label={t`Copy`}
					title={t`Copy`}
					onClick={() => copyToClipboard(url)}
				>
					<CopyIcon className="size-4" />
				</Button>
				{!isReadOnlyUser() && (
					<AlertDialog>
						<AlertDialogTrigger asChild>
							<Button
								type="button"
								variant="outline"
								size="icon"
								className="shrink-0"
								disabled={regenerating}
								aria-label={t`Regenerate`}
								title={t`Regenerate`}
							>
								<RefreshCwIcon className={cn("size-4", regenerating && "animate-spin")} />
							</Button>
						</AlertDialogTrigger>
						<AlertDialogContent>
							<AlertDialogHeader>
								<AlertDialogTitle>
									<Trans>Regenerate push URL?</Trans>
								</AlertDialogTitle>
								<AlertDialogDescription>
									<Trans>The current URL will stop working. Anything using it must be updated with the new URL.</Trans>
								</AlertDialogDescription>
							</AlertDialogHeader>
							<AlertDialogFooter>
								<AlertDialogCancel>
									<Trans>Cancel</Trans>
								</AlertDialogCancel>
								<AlertDialogAction className={cn(buttonVariants({ variant: "destructive" }))} onClick={regenerate}>
									<Trans>Regenerate</Trans>
								</AlertDialogAction>
							</AlertDialogFooter>
						</AlertDialogContent>
					</AlertDialog>
				)}
			</div>
			<p className="text-xs text-muted-foreground">
				<Trans>
					Send a request to this URL at least once per interval. Optional parameters:{" "}
					<code className="font-mono">status=up|down</code>, <code className="font-mono">msg</code>,{" "}
					<code className="font-mono">ping</code> (ms).
				</Trans>
			</p>
		</div>
	)
}
