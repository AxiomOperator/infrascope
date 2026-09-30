import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { BellIcon, LoaderCircleIcon, SaveIcon } from "lucide-react"
import type { ClientResponseError } from "pocketbase"
import { useEffect, useState } from "react"
import { PushNotifications } from "@/components/push-notifications"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { Switch } from "@/components/ui/switch"
import { toast } from "@/components/ui/use-toast"
import { parseReminderMinutes, REMINDER_MAX_MINUTES, REMINDER_MIN_MINUTES } from "@/lib/alert-ack"
import { isReadOnlyUser, saveUserSettings } from "@/lib/api"
import { normalizeTemplate, templateTooLong } from "@/lib/notification-channels"
import type { NotificationTemplate, UserSettings } from "@/types"
import { NotificationChannels } from "./notification-channels"
import { TemplateEditor } from "./notification-templates"
import { QuietHours } from "./quiet-hours"

const sameTemplate = (a: NotificationTemplate | null, b: NotificationTemplate | null) =>
	(a?.title ?? "") === (b?.title ?? "") && (a?.body ?? "") === (b?.body ?? "")

const SettingsNotificationsPage = ({ userSettings }: { userSettings: UserSettings }) => {
	const [reminder, setReminder] = useState(String(userSettings.reminderMinutes || ""))
	const reminderMinutes = parseReminderMinutes(reminder)
	const [templates, setTemplates] = useState<NotificationTemplate>(userSettings.templates ?? {})
	const [bypass, setBypass] = useState(!!userSettings.criticalBypassQuietHours)
	const [isLoading, setIsLoading] = useState(false)
	const readOnly = isReadOnlyUser()

	// update values when userSettings changes
	useEffect(() => {
		setReminder(String(userSettings.reminderMinutes || ""))
		setTemplates(userSettings.templates ?? {})
		setBypass(!!userSettings.criticalBypassQuietHours)
	}, [userSettings])

	const isDirty =
		reminderMinutes !== (userSettings.reminderMinutes || 0) ||
		!sameTemplate(normalizeTemplate(templates), normalizeTemplate(userSettings.templates)) ||
		bypass !== !!userSettings.criticalBypassQuietHours

	async function updateSettings() {
		const fail = (description: string) =>
			toast({ title: t`Failed to save settings`, description, variant: "destructive" })
		if (reminderMinutes === null) {
			fail(
				t`Enter a reminder interval between ${REMINDER_MIN_MINUTES} and ${REMINDER_MAX_MINUTES} minutes, or leave it blank.`
			)
			return
		}
		if (templateTooLong(templates).length) {
			fail(t`Templates can be at most 2000 characters.`)
			return
		}
		setIsLoading(true)
		try {
			await saveUserSettings({
				reminderMinutes,
				templates: normalizeTemplate(templates) ?? {},
				criticalBypassQuietHours: bypass,
			})
			toast({ title: t`Settings saved`, description: t`Your user settings have been updated.` })
		} catch (e: unknown) {
			fail((e as ClientResponseError).response?.message || (e as Error).message)
		}
		setIsLoading(false)
	}

	return (
		<div>
			<div>
				<h3 className="text-xl font-medium mb-2">
					<Trans>Notifications</Trans>
				</h3>
				<p className="text-sm text-muted-foreground leading-relaxed">
					<Trans>Configure how you receive alert notifications.</Trans>
				</p>
				<p className="text-sm text-muted-foreground mt-1.5 leading-relaxed">
					<Trans>
						Looking instead for where to create alerts? Click the bell <BellIcon className="inline h-4 w-4" /> icons in
						the systems table.
					</Trans>
				</p>
			</div>
			<Separator className="my-4" />
			<div className="space-y-5">
				<NotificationChannels userSettings={userSettings} />
				<Separator />
				<PushNotifications />
				<Separator />
				<div className="grid gap-2">
					<div className="mb-1">
						<h3 className="mb-1 text-lg font-medium">
							<Trans>Message templates</Trans>
						</h3>
						<p className="text-sm text-muted-foreground leading-relaxed">
							<Trans>
								Default title and body of your notifications. Channels can override them. Invalid templates fall back to
								the built-in message.
							</Trans>
						</p>
					</div>
					<TemplateEditor idPrefix="global-tpl" value={templates} onChange={setTemplates} disabled={readOnly} />
				</div>
				<Separator />
				<div className="grid gap-2">
					<div className="mb-1">
						<h3 className="mb-1 text-lg font-medium">
							<Trans>Reminders</Trans>
						</h3>
						<p className="text-sm text-muted-foreground leading-relaxed">
							<Trans>
								Repeat notifications for unacknowledged alerts every N minutes until they are acknowledged or resolved
								(at most 24 times). Quiet hours apply.
							</Trans>
						</p>
					</div>
					<Label className="block" htmlFor="reminder-minutes">
						<Trans>Reminder interval (minutes)</Trans>
					</Label>
					<Input
						id="reminder-minutes"
						type="number"
						inputMode="numeric"
						min={REMINDER_MIN_MINUTES}
						max={REMINDER_MAX_MINUTES}
						step={1}
						value={reminder}
						onChange={(e) => setReminder(e.target.value)}
						placeholder={t`Off`}
						className="w-40"
						aria-invalid={reminderMinutes === null}
					/>
					<p
						className={
							reminderMinutes === null ? "text-[0.8rem] text-destructive" : "text-[0.8rem] text-muted-foreground"
						}
					>
						<Trans>
							Between {REMINDER_MIN_MINUTES} and {REMINDER_MAX_MINUTES}. Leave blank or 0 to disable reminders.
						</Trans>
					</p>
				</div>
				<Separator />
				<div className="space-y-3">
					<QuietHours />
					<label htmlFor="critical-bypass" className="flex items-start justify-between gap-4 rounded-md border p-3">
						<span className="grid gap-1">
							<span className="text-sm font-medium">
								<Trans>Critical alerts bypass quiet hours</Trans>
							</span>
							<span className="text-[0.8rem] text-muted-foreground">
								<Trans>Deliver critical alerts, such as a system or monitor going down, even during quiet hours.</Trans>
							</span>
						</span>
						<Switch id="critical-bypass" checked={bypass} onCheckedChange={setBypass} disabled={readOnly} />
					</label>
				</div>
				<Separator />
				<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
					<Button
						type="button"
						className="flex items-center gap-1.5 disabled:opacity-100"
						onClick={updateSettings}
						disabled={isLoading}
					>
						{isLoading ? <LoaderCircleIcon className="h-4 w-4 animate-spin" /> : <SaveIcon className="h-4 w-4" />}
						<Trans>Save Settings</Trans>
					</Button>
					{isDirty && (
						<output className="text-sm text-muted-foreground">
							<Trans>You have unsaved changes.</Trans>
						</output>
					)}
				</div>
			</div>
		</div>
	)
}

export default SettingsNotificationsPage
