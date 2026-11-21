Generated runtime.zip is embedded by make build / make dist. It contains the
locked, production-only Node SDK and dependencies; Node itself is not bundled.
Direct go build remains useful for Go development, but cannot start supplied
agents until make agent-runtime has generated this payload.
