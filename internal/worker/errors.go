package worker

const (
	ErrorCodeInvalidIntent       = "INVALID_INTENT"
	ErrorCodeAlreadyExpired      = "ALREADY_EXPIRED"
	ErrorCodeSessionTimeout      = "SESSION_TIMEOUT"
	ErrorCodeWorkerShutdown      = "WORKER_SHUTDOWN"
	ErrorCodeShareNotFound       = "SHARE_NOT_FOUND"
	ErrorCodeInvalidSharePayload = "INVALID_SHARE_PAYLOAD"
	ErrorCodeShareMetadata       = "SHARE_METADATA_MISMATCH"
	ErrorCodeMissingPublicKey    = "DKG_MISSING_PUBLIC_KEY"
	ErrorCodeMissingAddress      = "DKG_MISSING_ADDRESS"
	ErrorCodeProtocol            = "MPC_PROTOCOL_ERROR"
	ErrorCodeInternal            = "INTERNAL_ERROR"
)
