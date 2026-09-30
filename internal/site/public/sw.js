// InfraScope service worker: shows browser (Web Push) notifications.
// It deliberately does no caching or offline handling, so the app is never
// served stale.

self.addEventListener("install", () => {
	self.skipWaiting()
})

self.addEventListener("activate", (event) => {
	event.waitUntil(self.clients.claim())
})

/** Resolves a notification URL against the app scope; only http(s) URLs are opened. */
function resolveUrl(url) {
	const scope = self.registration.scope
	try {
		const resolved = new URL(url || "", scope)
		if (resolved.protocol === "http:" || resolved.protocol === "https:") {
			return resolved.href
		}
	} catch {
		// fall through
	}
	return scope
}

self.addEventListener("push", (event) => {
	let data = {}
	try {
		data = event.data ? event.data.json() : {}
	} catch {
		data = { body: event.data ? event.data.text() : "" }
	}
	const title = data.title || "InfraScope"
	const icon = new URL("static/icon.png", self.registration.scope).href
	const options = {
		body: data.body || "",
		icon,
		badge: icon,
		data: { url: resolveUrl(data.url) },
		requireInteraction: !!data.urgent,
	}
	if (data.tag) {
		options.tag = data.tag
		options.renotify = true
	}
	event.waitUntil(self.registration.showNotification(title, options))
})

self.addEventListener("notificationclick", (event) => {
	event.notification.close()
	const url = event.notification.data?.url || self.registration.scope
	event.waitUntil(
		(async () => {
			const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true })
			// prefer a window already showing the URL, then any window of the app
			const exact = windows.find((client) => client.url === url)
			const client = exact || windows.find((client) => client.url.startsWith(self.registration.scope))
			if (client) {
				await client.focus()
				if (!exact && "navigate" in client) {
					try {
						await client.navigate(url)
					} catch {
						// navigation of uncontrolled windows can fail; focusing is enough
					}
				}
				return
			}
			await self.clients.openWindow(url)
		})()
	)
})
