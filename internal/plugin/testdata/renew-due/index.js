// A plugin whose renewal leaves the sign-in still due: its refreshLead
// is an hour, and every renewal it can't really make pushes expires out
// by half an hour only, which stays inside that lead. The refresh token
// says what the renewal gives back: "d-keep…" keeps the token it has and
// gives a shorter expires, "d-new…" gives a new token with the same
// short expires, and "d-none…" gives nothing at all. Each renewal is
// written to $RENEW_LOG as a line naming the refresh token it was
// given, so a test counts the renewals a listing or a keep-alive makes.
import fs from "node:fs"

const LEAD = 60 * 60 * 1000

export const RenewDuePlugin = async () => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider.dueco = {
      name: "DueCo",
      npm: "@ai-sdk/openai-compatible",
      api: "https://due.invalid/v1",
      models: { "due-1": { name: "Due One", limit: { context: 1000, output: 100 } } },
    }
  },
  provider: {
    id: "dueco",
    models: async (provider) => provider.models,
  },
  auth: {
    provider: "dueco",
    refreshLead: LEAD,
    methods: [{ type: "api", label: "API key" }],
    async refresh(auth) {
      fs.appendFileSync(process.env.RENEW_LOG, auth.refresh + "\n")
      const expires = Date.now() + 30 * 60 * 1000
      if (auth.refresh.startsWith("d-none")) return undefined
      if (auth.refresh.startsWith("d-new")) return { access: auth.access + "+", refresh: auth.refresh + "+", expires }
      return { expires }
    },
  },
})
