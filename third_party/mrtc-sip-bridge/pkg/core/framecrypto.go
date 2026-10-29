package core

// FrameCrypto encrypts and decrypts MatrixRTC media frames (SFrame, LiveKit trailer format).
// A host supplies it for rooms where clients negotiate SFrame E2EE; nil means plaintext media.
type FrameCrypto interface {
	// Sealer returns the function that encrypts one outgoing media frame.
	Sealer() func(payload []byte) ([]byte, error)
	// Opener returns the function that decrypts one frame from the given participant identity.
	// nil means the sender cannot be decrypted (for example, its keys have not arrived yet).
	Opener(identity string) func(frame []byte) ([]byte, error)
}
