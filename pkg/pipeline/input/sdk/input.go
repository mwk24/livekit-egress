package sdk

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/pipeline/input/builder"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	AudioAppSource = "audioAppSrc"
	VideoAppSource = "videoAppSrc"

	subscriptionTimeout = time.Second * 5
)

type SDKInput struct {
	*builder.InputBin

	room   *lksdk.Room
	logger logger.Logger
	cs     *Synchronizer

	// track
	trackID string

	// track composite
	audioTrackID string
	videoTrackID string

	// participant
	participantIdentity string

	// composite audio source
	audioSrc         *app.Source
	audioCodec       webrtc.RTPCodecParameters
	audioWriter      *appWriter
	audioPlaying     chan struct{}
	audioParticipant string

	// composite video source
	videoSrc         *app.Source
	videoCodec       webrtc.RTPCodecParameters
	videoWriter      *appWriter
	videoPlaying     chan struct{}
	videoParticipant string

	active         atomic.Int32
	mutedChan      chan bool
	endRecording   chan struct{}
	startRecording chan struct{}
}

func NewSDKInputWithPresubscription(ctx context.Context, p *params.Params) (*SDKInput, error) {
	ctx, span := tracer.Start(ctx, "SDKInput.NewWithPreSubscribedRoom")
	defer span.End()

	s := &SDKInput{
		logger:         p.Logger,
		cs:             &Synchronizer{},
		mutedChan:      p.MutedChan,
		endRecording:   make(chan struct{}),
		startRecording: make(chan struct{}),
	}

	// Now setup the data, we assume both audio and video are subscribed
	// and we can just use the first track for each
	var err error
	rp := p.SourceParams.Participant
	for _, track := range rp.Tracks() {

		println("track", track.Name(), track.Kind().String())

		// Track must be subscribed
		// if !track.IsSubscribed() {
		// 	return nil, nil
		// }

		src, err := gst.NewElementWithName("appsrc", "") // need name?
		if err != nil {
			s.logger.Errorw("could not create appsrc", err)
			return nil, err
		}

		webrtcTrack := track.Track().(*webrtc.TrackRemote)
		codecMimeType := params.MimeType(strings.ToLower(webrtcTrack.Codec().MimeType)) // !!!
		writeBlanks := true

		switch webrtcTrack.Kind() {
		case webrtc.RTPCodecTypeAudio:
			s.audioSrc = app.SrcFromElement(src)
			s.audioPlaying = make(chan struct{})
			s.audioCodec = webrtcTrack.Codec()
			s.audioWriter, err = NewAppWriter(webrtcTrack, codecMimeType, rp, s.logger, s.audioSrc, s.cs, s.audioPlaying, writeBlanks)
			s.audioParticipant = rp.Identity()
			if err != nil {
				s.logger.Errorw("could not create app writer", err)
				return nil, err
			}

		case webrtc.RTPCodecTypeVideo:
			s.videoSrc = app.SrcFromElement(src)
			s.videoPlaying = make(chan struct{})
			s.videoCodec = webrtcTrack.Codec()
			s.videoWriter, err = NewAppWriter(webrtcTrack, codecMimeType, rp, s.logger, s.videoSrc, s.cs, s.videoPlaying, writeBlanks)
			s.videoParticipant = rp.Identity()
			if err != nil {
				s.logger.Errorw("could not create app writer", err)
				return nil, err
			}
		}
	}

	input, err := builder.NewSDKInput(ctx, p, s.audioSrc, s.videoSrc, s.audioCodec, s.videoCodec)
	if err != nil {
		// Log error
		println(err)
		return nil, err
	}
	s.InputBin = input

	return s, nil
}

func NewSDKInput(ctx context.Context, p *params.Params) (*SDKInput, error) {
	ctx, span := tracer.Start(ctx, "SDKInput.New")
	defer span.End()

	s := &SDKInput{
		logger:         p.Logger,
		cs:             &Synchronizer{},
		mutedChan:      p.MutedChan,
		endRecording:   make(chan struct{}),
		startRecording: make(chan struct{}),
	}

	if err := s.joinRoom(p); err != nil {
		return nil, err
	}

	input, err := builder.NewSDKInput(ctx, p, s.audioSrc, s.videoSrc, s.audioCodec, s.videoCodec)
	if err != nil {
		return nil, err
	}
	s.InputBin = input

	return s, nil
}

func (s *SDKInput) StartRecording() chan struct{} {
	return nil
}

func (s *SDKInput) GetStartTime() int64 {
	return s.cs.startTime.Load()
}

func (s *SDKInput) Playing(name string) {
	var playing chan struct{}

	if name == AudioAppSource {
		playing = s.audioPlaying
	} else if name == VideoAppSource {
		playing = s.videoPlaying
	} else {
		return
	}

	select {
	case <-playing:
		return
	default:
		close(playing)
	}
}

func (s *SDKInput) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKInput) GetEndTime() int64 {
	return s.cs.endTime.Load() + s.cs.delay.Load()
}

func (s *SDKInput) SendEOS() {
	s.cs.SetEndTime(time.Now().UnixNano())

	var wg sync.WaitGroup
	if s.audioWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.audioWriter.sendEOS()
			s.logger.Debugw("audio writer finished")
		}()
	}
	if s.videoWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.videoWriter.sendEOS()
			s.logger.Debugw("video writer finished")
		}()
	}
	wg.Wait()
}

func (s *SDKInput) SendAppSrcEOS(name string) {
	if name == AudioAppSource {
		s.audioWriter.sendEOS()
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	} else if name == VideoAppSource {
		s.videoWriter.sendEOS()
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	}
}

func (s *SDKInput) Close() {
	s.room.Disconnect()
}
