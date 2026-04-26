package minimax

// MiniMax Token Plan models.
// Text models follow the current OpenAI-compatible text API docs.
// Audio / image / video / music models are merged here because MiniMax uses a
// single channel type while different relay flows dispatch by request path.

var ModelList = []string{
	"MiniMax-M2.7",
	"MiniMax-M2.7-highspeed",
	"MiniMax-M2.5",
	"MiniMax-M2.5-highspeed",
	"MiniMax-M2.1",
	"MiniMax-M2.1-highspeed",
	"MiniMax-M2",
	"M2-her",
	"speech-2.8-hd",
	"speech-2.8-turbo",
	"speech-2.6-hd",
	"speech-2.6-turbo",
	"speech-2.5-hd-preview",
	"speech-2.5-turbo-preview",
	"speech-02-hd",
	"speech-02-turbo",
	"speech-01-hd",
	"speech-01-turbo",
	"image-01",
	"image-01-live",
	"MiniMax-Hailuo-2.3",
	"MiniMax-Hailuo-2.3-Fast",
	"MiniMax-Hailuo-02",
	"T2V-01-Director",
	"T2V-01",
	"I2V-01-Director",
	"I2V-01-live",
	"I2V-01",
	"S2V-01",
	"music-2.5",
	"music-2.5+",
	"music-2.6",
}

var ChannelName = "minimax"
