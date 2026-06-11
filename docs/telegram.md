# Telegram Setup

VORTEX can be driven entirely from Telegram: send the agent a message, approve
tool actions, and receive alerts (secret expiry, burst bans, self-healing
events) on your phone.

## 1. Create a bot

1. Open [@BotFather](https://t.me/BotFather) in Telegram.
2. Send `/newbot`, choose a name and username.
3. Copy the **bot token** it gives you (looks like `123456:ABC-DEF…`).

## 2. Find your chat ID

1. Send any message to your new bot.
2. Visit `https://api.telegram.org/bot<TOKEN>/getUpdates`.
3. Copy the numeric `chat.id` from the JSON.

## 3. Configure VORTEX

Either run the wizard:

```bash
vortex setup     # answer "y" to the Telegram step
```

…or set environment variables before `vortex start`:

```bash
export VORTEX_TELEGRAM_TOKEN="123456:ABC-DEF..."
export VORTEX_TELEGRAM_DEFAULT_CHAT="987654321"
# optional: restrict who may command the bot
export VORTEX_TELEGRAM_ALLOWED_IDS="987654321,111222333"
```

Environment variables override the saved setup config.

## 4. Webhook vs polling

- **Webhook** (recommended for a public server): set `VORTEX_PUBLIC_URL` to
  your HTTPS base URL; VORTEX registers `https://<host>/webhook/telegram`.
- **Polling** (for a box with no public URL): set
  `VORTEX_TELEGRAM_POLLING=true`.

The webhook verifies Telegram's secret token and is per-IP rate limited.

## 5. Use it

Message the bot:

> _"Restart nginx on web-1 and confirm it's up."_

When the agent wants to run a machine-touching action it asks for approval;
reply to approve or reject. Alerts (e.g. `⚠️ Secret EXPIRED: db_password`)
arrive automatically based on the notification rules.

## Security notes

- Always set `VORTEX_TELEGRAM_ALLOWED_IDS` on a server so only you can command
  the bot.
- The bot token is a credential — keep it in the environment or the encrypted
  setup config, never in `vortex.cue` or version control.
