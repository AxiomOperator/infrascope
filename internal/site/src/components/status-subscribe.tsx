import { t } from "@lingui/core/macro"
import { Plural, Trans } from "@lingui/react/macro"
import { BellIcon, CheckCircle2Icon, LoaderCircleIcon, Trash2Icon } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useState } from "react"
import { getErrorMessage } from "@/components/network-monitors-table/monitor-form-utils"
import { prependBasePath } from "@/components/router"
import { Button } from "@/components/ui/button"
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { toast } from "@/components/ui/use-toast"
import { pb } from "@/lib/api"
import { formatShortDate } from "@/lib/utils"

/** Same limit as status_subscribers.email. */
const MAX_EMAIL_LENGTH = 254

type SubscribeState = "idle" | "sending" | "done"

/**
 * "Subscribe to updates" button of a public status page. The request is sent
 * without authentication; the hub answers the same way for every address.
 */
export function StatusSubscribeButton({ slug, title }: { slug: string; title: string }) {
	const [open, setOpen] = useState(false)
	const [email, setEmail] = useState("")
	// honeypot: hidden from people, filled in by some bots
	const [website, setWebsite] = useState("")
	const [state, setState] = useState<SubscribeState>("idle")
	const [error, setError] = useState("")

	const onOpenChange = (next: boolean) => {
		setOpen(next)
		if (next) {
			setState("idle")
			setError("")
		}
	}

	const submit = async (e: FormEvent) => {
		e.preventDefault()
		setState("sending")
		setError("")
		try {
			const res = await fetch(prependBasePath(`/api/beszel/status-pages/${encodeURIComponent(slug)}/subscribe`), {
				method: "POST",
				headers: { "Content-Type": "application/json", Accept: "application/json" },
				body: JSON.stringify({ email: email.trim(), website }),
			})
			if (res.ok) {
				setState("done")
				setEmail("")
				return
			}
			if (res.status === 429) setError(t`Too many requests. Please try again later.`)
			else if (res.status === 400) setError(t`Enter a valid email address.`)
			else setError(t`Something went wrong. Please try again later.`)
		} catch {
			setError(t`Something went wrong. Please try again later.`)
		}
		setState("idle")
	}

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogTrigger asChild>
				<Button variant="outline" size="sm" className="h-9 gap-1.5">
					<BellIcon className="size-4" />
					<span className="sr-only sm:not-sr-only">
						<Trans>Subscribe to updates</Trans>
					</span>
				</Button>
			</DialogTrigger>
			<DialogContent className="max-w-md">
				<DialogHeader>
					<DialogTitle>
						<Trans>Subscribe to updates</Trans>
					</DialogTitle>
					<DialogDescription>
						<Trans>Get an email when {title} reports an incident or an outage.</Trans>
					</DialogDescription>
				</DialogHeader>
				{state === "done" ? (
					<div className="flex items-start gap-2.5 rounded-md border border-green-500/30 bg-green-500/10 p-3 text-sm text-green-700 dark:text-green-400">
						<CheckCircle2Icon className="size-4 shrink-0 mt-0.5" />
						<p>
							<Trans>Check your inbox and confirm the subscription with the link we sent you.</Trans>
						</p>
					</div>
				) : (
					<form onSubmit={submit} className="grid gap-3">
						<div className="grid gap-2">
							<Label htmlFor="status-subscribe-email">
								<Trans>Email</Trans>
							</Label>
							<Input
								id="status-subscribe-email"
								type="email"
								autoComplete="email"
								required
								maxLength={MAX_EMAIL_LENGTH}
								value={email}
								onChange={(e) => setEmail(e.target.value)}
								aria-invalid={!!error}
							/>
						</div>
						<div aria-hidden="true" className="absolute -left-[9999px] size-px overflow-hidden">
							<label htmlFor="status-subscribe-website">Website</label>
							<input
								id="status-subscribe-website"
								name="website"
								type="text"
								tabIndex={-1}
								autoComplete="off"
								value={website}
								onChange={(e) => setWebsite(e.target.value)}
							/>
						</div>
						{error && <p className="text-sm text-destructive">{error}</p>}
						<p className="text-xs text-muted-foreground">
							<Trans>Every email has a link to unsubscribe.</Trans>
						</p>
						<DialogFooter>
							<Button type="submit" disabled={state === "sending"}>
								{state === "sending" && <LoaderCircleIcon className="size-4 animate-spin" />}
								<Trans>Subscribe</Trans>
							</Button>
						</DialogFooter>
					</form>
				)}
			</DialogContent>
		</Dialog>
	)
}

interface Subscriber {
	id: string
	email: string
	confirmed: boolean
	createdAt: string
	confirmedAt: string
	lastSentAt: string
}

interface SubscriberList {
	total: number
	confirmed: number
	page: number
	perPage: number
	items: Subscriber[]
}

/** Subscriber count and list of a status page, for its owner. */
export function StatusPageSubscribers({ pageId }: { pageId: string }) {
	const [data, setData] = useState<SubscriberList | null>(null)
	const [items, setItems] = useState<Subscriber[]>([])
	const [loading, setLoading] = useState(false)
	const [expanded, setExpanded] = useState(false)
	const [hasMore, setHasMore] = useState(false)

	const load = useCallback(
		async (page: number) => {
			setLoading(true)
			try {
				const res: SubscriberList = await pb.send(
					`/api/beszel/status-pages/${encodeURIComponent(pageId)}/subscribers`,
					{ query: { page } }
				)
				setData(res)
				setHasMore(res.items.length === res.perPage)
				setItems((current) => (page === 1 ? res.items : [...current, ...res.items]))
			} catch (e) {
				toast({ variant: "destructive", title: t`Error`, description: getErrorMessage(e) })
			} finally {
				setLoading(false)
			}
		},
		[pageId]
	)

	useEffect(() => {
		load(1)
	}, [load])

	const remove = async (subscriber: Subscriber) => {
		try {
			await pb.send(
				`/api/beszel/status-pages/${encodeURIComponent(pageId)}/subscribers/${encodeURIComponent(subscriber.id)}`,
				{ method: "DELETE" }
			)
			setItems((current) => current.filter((s) => s.id !== subscriber.id))
			setData((current) =>
				current
					? {
							...current,
							total: current.total - 1,
							confirmed: current.confirmed - (subscriber.confirmed ? 1 : 0),
						}
					: current
			)
		} catch (e) {
			toast({ variant: "destructive", title: t`Error`, description: getErrorMessage(e) })
		}
	}

	if (!data) {
		return null
	}
	const pending = data.total - data.confirmed
	return (
		<div className="grid gap-2 rounded-md border p-3 text-sm">
			<div className="flex items-center justify-between gap-2">
				<span>
					<Plural value={data.confirmed} one="# subscriber" other="# subscribers" />
					{pending > 0 && (
						<span className="ms-1.5 text-muted-foreground">
							(<Trans>{pending} unconfirmed</Trans>)
						</span>
					)}
				</span>
				{data.total > 0 && (
					<Button type="button" variant="ghost" size="sm" className="h-7" onClick={() => setExpanded(!expanded)}>
						{expanded ? <Trans>Hide</Trans> : <Trans>Show</Trans>}
					</Button>
				)}
			</div>
			{expanded && items.length > 0 && (
				<ul className="max-h-60 overflow-y-auto divide-y rounded-md border">
					{items.map((subscriber) => (
						<li key={subscriber.id} className="flex items-center gap-2 ps-3 pe-1 py-1 min-w-0">
							<span className="min-w-0 flex-1 truncate">{subscriber.email}</span>
							<span className="shrink-0 text-xs text-muted-foreground">
								{subscriber.confirmed ? formatShortDate(subscriber.confirmedAt) : <Trans>Unconfirmed</Trans>}
							</span>
							<Button
								type="button"
								variant="ghost"
								size="icon"
								className="size-7 shrink-0"
								title={t`Remove`}
								onClick={() => remove(subscriber)}
							>
								<Trash2Icon className="size-3.5" />
								<span className="sr-only">
									<Trans>Remove</Trans>
								</span>
							</Button>
						</li>
					))}
				</ul>
			)}
			{expanded && hasMore && (
				<Button type="button" variant="outline" size="sm" disabled={loading} onClick={() => load(data.page + 1)}>
					<Trans>Show more</Trans>
				</Button>
			)}
		</div>
	)
}
