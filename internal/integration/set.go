package integration

type Set struct {
	Sources          []Source
	Workspace        WorkspaceProvider
	Multiplexer      MultiplexerProvider
	ActionProviders  []ActionProvider
	CleanupProviders []CleanupProvider
}
