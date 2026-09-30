/** Pure helpers for browser (Web Push) notifications. */

/** A push subscription of the current user (push_subscriptions record). */
export interface PushDevice {
	id: string
	endpoint: string
	name: string
	userAgent: string
	createdAt: string
	lastSuccessAt: string
	failures: number
}

/** Why push notifications can or can't be used in this browser. */
export type PushSupport = "supported" | "insecure" | "unsupported"

interface PushEnvironment {
	isSecureContext?: boolean
	hasServiceWorker: boolean
	hasPushManager: boolean
	hasNotification: boolean
}

/** Reports whether the environment can receive push notifications. Push requires HTTPS (or localhost). */
export function pushSupport(env: PushEnvironment): PushSupport {
	if (env.isSecureContext === false) {
		return "insecure"
	}
	if (!env.hasServiceWorker || !env.hasPushManager || !env.hasNotification) {
		return "unsupported"
	}
	return "supported"
}

/** The push environment of the current browser. */
export function currentPushSupport(): PushSupport {
	if (typeof window === "undefined") {
		return "unsupported"
	}
	return pushSupport({
		isSecureContext: window.isSecureContext,
		hasServiceWorker: "serviceWorker" in navigator,
		hasPushManager: "PushManager" in window,
		hasNotification: "Notification" in window,
	})
}

/** Decodes a base64url string (such as a VAPID public key) to bytes. */
export function urlBase64ToUint8Array(value: string): Uint8Array<ArrayBuffer> {
	const padding = "=".repeat((4 - (value.length % 4)) % 4)
	const base64 = (value + padding).replaceAll("-", "+").replaceAll("_", "/")
	const raw = atob(base64)
	const bytes = new Uint8Array(new ArrayBuffer(raw.length))
	for (let i = 0; i < raw.length; i++) {
		bytes[i] = raw.charCodeAt(i)
	}
	return bytes
}

/** Reports whether two keys (bytes or base64url) are equal. */
export function sameKey(a: ArrayBuffer | Uint8Array | null | undefined, b: Uint8Array): boolean {
	if (!a) {
		return false
	}
	const bytes = a instanceof Uint8Array ? a : new Uint8Array(a)
	return bytes.length === b.length && bytes.every((value, i) => value === b[i])
}

/** Returns a device label such as "Firefox on Linux" from a user agent. */
export function defaultDeviceName(userAgent: string): string {
	const ua = userAgent || ""
	let browser = "Browser"
	if (/Edg(e|A|iOS)?\//.test(ua)) browser = "Edge"
	else if (/OPR\/|Opera/.test(ua)) browser = "Opera"
	else if (/Firefox\/|FxiOS\//.test(ua)) browser = "Firefox"
	else if (/Chrome\/|CriOS\//.test(ua)) browser = "Chrome"
	else if (/Safari\//.test(ua)) browser = "Safari"

	let os = ""
	if (/iPhone|iPad|iPod/.test(ua)) os = "iOS"
	else if (/Android/.test(ua)) os = "Android"
	else if (/Windows/.test(ua)) os = "Windows"
	else if (/Mac OS X|Macintosh/.test(ua)) os = "macOS"
	else if (/CrOS/.test(ua)) os = "ChromeOS"
	else if (/Linux/.test(ua)) os = "Linux"

	return os ? `${browser} on ${os}` : browser
}

/** The service worker URL and scope for an app base path ("" or "/" or "/hub/"). */
export function serviceWorkerLocation(basePath: string): { url: string; scope: string } {
	const scope = `/${basePath.replace(/^\/+|\/+$/g, "")}/`.replace("//", "/")
	return { url: `${scope}sw.js`, scope }
}
