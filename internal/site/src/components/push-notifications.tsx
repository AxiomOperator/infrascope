import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { BellOffIcon, BellRingIcon, LoaderCircleIcon, SendIcon, Trash2Icon } from "lucide-react"
import { useCallback, useEffect, useState } from "react"
import { basePath } from "@/components/router"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { toast } from "@/components/ui/use-toast"
import { pb } from "@/lib/api"
import {
	currentPushSupport,
	defaultDeviceName,
	type PushDevice,
	sameKey,
	serviceWorkerLocation,
	urlBase64ToUint8Array,
} from "@/lib/push-notifications"

const api = "/api/beszel/push-subscriptions"

function errorMessage(error: unknown): string {
	return (
		(error as { response?: { message?: string } })?.response?.message ||
		(error as Error)?.message ||
		t`Please check logs for more details.`
	)
}

function showError(title: string, error: unknown) {
	console.error(error)
	toast({ variant: "destructive", title, description: errorMessage(error) })
}

/** Registers the service worker at the app base path and waits until it is active. */
async function registerServiceWorker(): Promise<ServiceWorkerRegistration> {
	const { url, scope } = serviceWorkerLocation(basePath)
	await navigator.serviceWorker.register(url, { scope, updateViaCache: "none" })
	return navigator.serviceWorker.ready
}

/** The push subscription of this browser, if the service worker is registered. */
async function currentSubscription(): Promise<PushSubscription | null> {
	const { scope } = serviceWorkerLocation(basePath)
	const registration = await navigator.serviceWorker.getRegistration(scope)
	return (await registration?.pushManager.getSubscription()) ?? null
}

/**
 * Browser notifications on this device: enable/disable Web Push for the
 * current browser, send a test, and manage the user's other devices.
 */
export function PushNotifications() {
	const [support] = useState(currentPushSupport)
	const [permission, setPermission] = useState<NotificationPermission | "unsupported">(() =>
		typeof Notification === "undefined" ? "unsupported" : Notification.permission
	)
	const [endpoint, setEndpoint] = useState<string | null>(null)
	const [devices, setDevices] = useState<PushDevice[]>([])
	const [name, setName] = useState(() => defaultDeviceName(typeof navigator === "undefined" ? "" : navigator.userAgent))
	const [busy, setBusy] = useState<string | null>(null)

	const loadDevices = useCallback(async () => {
		try {
			setDevices(await pb.collection("push_subscriptions").getFullList<PushDevice>({ sort: "-createdAt" }))
		} catch (error) {
			console.error(error)
		}
	}, [])

	useEffect(() => {
		loadDevices()
		if (support === "supported") {
			currentSubscription()
				.then((subscription) => setEndpoint(subscription?.endpoint ?? null))
				.catch((error) => console.error(error))
		}
	}, [support, loadDevices])

	const thisDevice = endpoint ? devices.find((device) => device.endpoint === endpoint) : undefined
	const enabled = !!thisDevice && permission === "granted"

	async function enable() {
		setBusy("enable")
		try {
			const result = await Notification.requestPermission()
			setPermission(result)
			if (result !== "granted") {
				toast({
					variant: "destructive",
					title: t`Notifications are blocked`,
					description: t`Allow notifications for this site in your browser settings, then try again.`,
				})
				return
			}
			const vapid = await pb.send<{ enabled: boolean; publicKey: string }>(`${api}/vapid-key`, {})
			if (!vapid.enabled || !vapid.publicKey) {
				toast({ variant: "destructive", title: t`Browser notifications are not available on this hub.` })
				return
			}
			const key = urlBase64ToUint8Array(vapid.publicKey)
			const registration = await registerServiceWorker()
			let subscription = await registration.pushManager.getSubscription()
			// a subscription made with another key (e.g. a reset hub) can't be used
			if (subscription && !sameKey(subscription.options.applicationServerKey, key)) {
				await subscription.unsubscribe()
				subscription = null
			}
			subscription ??= await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
			await pb.send(api, {
				method: "POST",
				body: { subscription: subscription.toJSON(), name: name.trim() || defaultDeviceName(navigator.userAgent) },
			})
			setEndpoint(subscription.endpoint)
			await loadDevices()
			toast({ title: t`Browser notifications enabled` })
		} catch (error) {
			showError(t`Failed to enable browser notifications`, error)
		} finally {
			setBusy(null)
		}
	}

	async function removeDevice(device: PushDevice) {
		setBusy(device.id)
		try {
			if (device.endpoint === endpoint) {
				const subscription = await currentSubscription()
				await subscription?.unsubscribe()
				setEndpoint(null)
			}
			await pb.send(`${api}/${encodeURIComponent(device.id)}`, { method: "DELETE" })
			await loadDevices()
		} catch (error) {
			showError(t`Failed to remove device`, error)
		} finally {
			setBusy(null)
		}
	}

	async function disable() {
		if (thisDevice) {
			await removeDevice(thisDevice)
			return
		}
		setBusy("enable")
		try {
			await (await currentSubscription())?.unsubscribe()
			setEndpoint(null)
		} catch (error) {
			showError(t`Failed to disable browser notifications`, error)
		} finally {
			setBusy(null)
		}
	}

	async function sendTest(device: PushDevice) {
		setBusy(`test-${device.id}`)
		try {
			await pb.send(`${api}/${encodeURIComponent(device.id)}/test`, { method: "POST" })
			toast({ title: t`Test notification sent`, description: device.name || undefined })
		} catch (error) {
			showError(t`Failed to send test notification`, error)
			await loadDevices()
		} finally {
			setBusy(null)
		}
	}

	return (
		<div className="grid gap-3">
			<div>
				<h3 className="mb-1 text-lg font-medium">
					<Trans>Browser notifications on this device</Trans>
				</h3>
				<p className="text-sm text-muted-foreground leading-relaxed">
					<Trans>Receive alerts as system notifications from this browser, even when InfraScope is not open.</Trans>
				</p>
			</div>
			{support === "insecure" && (
				<p className="text-sm text-destructive leading-relaxed">
					<Trans>
						Browser notifications require a secure connection. Open InfraScope over HTTPS (or on localhost) to enable
						them.
					</Trans>
				</p>
			)}
			{support === "unsupported" && (
				<p className="text-sm text-muted-foreground leading-relaxed">
					<Trans>This browser does not support push notifications.</Trans>
				</p>
			)}
			{support === "supported" && (
				<>
					{permission === "denied" && (
						<p className="text-sm text-destructive leading-relaxed">
							<Trans>
								Notifications are blocked for this site. Allow them in your browser settings to enable browser
								notifications.
							</Trans>
						</p>
					)}
					{enabled ? (
						<div className="flex flex-wrap items-center gap-2">
							<span className="text-sm">
								<Trans>Enabled on this device</Trans>
								{thisDevice?.name ? ` (${thisDevice.name})` : ""}
							</span>
							<Button
								variant="outline"
								size="sm"
								className="gap-1.5"
								disabled={!!busy}
								onClick={() => thisDevice && sendTest(thisDevice)}
							>
								{busy === `test-${thisDevice?.id}` ? (
									<LoaderCircleIcon className="size-4 animate-spin" />
								) : (
									<SendIcon className="size-4" />
								)}
								<Trans>Send test</Trans>
							</Button>
							<Button variant="outline" size="sm" className="gap-1.5" disabled={!!busy} onClick={disable}>
								<BellOffIcon className="size-4" />
								<Trans>Disable</Trans>
							</Button>
						</div>
					) : (
						<div className="flex flex-wrap items-end gap-2">
							<div className="grid gap-1.5">
								<Label htmlFor="push-device-name">
									<Trans>Device name</Trans>
								</Label>
								<Input
									id="push-device-name"
									className="w-64 max-w-full"
									maxLength={100}
									value={name}
									onChange={(e) => setName(e.target.value)}
								/>
							</div>
							<Button className="gap-1.5" disabled={!!busy || permission === "denied"} onClick={enable}>
								{busy === "enable" ? (
									<LoaderCircleIcon className="size-4 animate-spin" />
								) : (
									<BellRingIcon className="size-4" />
								)}
								<Trans>Enable</Trans>
							</Button>
						</div>
					)}
				</>
			)}
			{devices.length > 0 && (
				<div className="grid gap-1.5">
					<Label>
						<Trans>Your devices</Trans>
					</Label>
					<ul className="grid gap-1.5">
						{devices.map((device) => (
							<li
								key={device.id}
								className="flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 text-sm"
							>
								<div className="min-w-0">
									<div className="font-medium truncate">
										{device.name || defaultDeviceName(device.userAgent)}
										{device.endpoint === endpoint && (
											<span className="ms-1.5 text-muted-foreground font-normal">
												<Trans>(this device)</Trans>
											</span>
										)}
									</div>
									{device.failures > 0 && (
										<div className="text-xs text-destructive">
											<Trans>{device.failures} failed deliveries</Trans>
										</div>
									)}
								</div>
								<div className="flex gap-1">
									<Button
										variant="ghost"
										size="icon"
										title={t`Send test`}
										aria-label={t`Send test`}
										disabled={!!busy}
										onClick={() => sendTest(device)}
									>
										{busy === `test-${device.id}` ? (
											<LoaderCircleIcon className="size-4 animate-spin" />
										) : (
											<SendIcon className="size-4" />
										)}
									</Button>
									<Button
										variant="ghost"
										size="icon"
										title={t`Remove`}
										aria-label={t`Remove`}
										disabled={!!busy}
										onClick={() => removeDevice(device)}
									>
										{busy === device.id ? (
											<LoaderCircleIcon className="size-4 animate-spin" />
										) : (
											<Trash2Icon className="size-4" />
										)}
									</Button>
								</div>
							</li>
						))}
					</ul>
				</div>
			)}
		</div>
	)
}
