package bot

// 12 Pre-written consistent error messages
const (
	ErrInvalidURL        = "❌ Invalid or unsupported URL. Please send a valid link from YouTube, Instagram, TikTok, or Twitter/X."
	ErrRateLimited       = "⏳ Slow down! You are sending requests too quickly. Please wait a few seconds."
	ErrQuotaExceeded     = "⚠️ You have reached your daily download limit. Upgrade your plan or redeem a license key with /redeem."
	ErrDownloadFailed    = "❌ Failed to download media. The content might be private, deleted, or geo-restricted."
	ErrFileTooLarge      = "📁 File exceeds Telegram's upload size limit (50 MB). Try downloading a shorter clip."
	ErrUnauthorized      = "🚫 You are not authorized to use this bot or command."
	ErrBanned            = "⛔ Your account has been suspended from using this service."
	ErrInvalidKey        = "🔑 Invalid or non-existent license key. Please check and try again."
	ErrKeyExpired        = "⏰ This license key has expired or has already been redeemed."
	ErrAlreadyDownloading = "🔄 You already have an active download in progress. Use /cancel to abort it."
	ErrCancelled         = "🛑 Download process was cancelled."
	ErrInternalServer    = "💥 An internal server error occurred while processing your request. Please try again later."
)

// ResolveErrorMessage converts an error code or message to user-facing string
func ResolveErrorMessage(errStr string) string {
	switch errStr {
	case "ERR_INVALID_URL":
		return ErrInvalidURL
	case "ERR_RATE_LIMITED":
		return ErrRateLimited
	case "ERR_QUOTA_EXCEEDED":
		return ErrQuotaExceeded
	case "ERR_DOWNLOAD_FAILED":
		return ErrDownloadFailed
	case "ERR_FILE_TOO_LARGE":
		return ErrFileTooLarge
	case "ERR_UNAUTHORIZED":
		return ErrUnauthorized
	case "ERR_BANNED":
		return ErrBanned
	case "ERR_INVALID_KEY":
		return ErrInvalidKey
	case "ERR_KEY_EXPIRED":
		return ErrKeyExpired
	case "ERR_ALREADY_DOWNLOADING":
		return ErrAlreadyDownloading
	case "ERR_CANCELLED":
		return ErrCancelled
	case "ERR_INTERNAL_SERVER":
		return ErrInternalServer
	default:
		return ErrInternalServer
	}
}
