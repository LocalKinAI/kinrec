// kinrec_writer.m — macOS screen + audio → mp4 recording pipeline.
//
// Pipeline:
//
//   SCStream  ──→ video CMSampleBuffer (BGRA) ──→ AVAssetWriterInput(video)
//   SCStream  ──→ audio CMSampleBuffer (PCM)  ──→ AVAssetWriterInput(audio)
//                                                        ↓
//                                                  AVAssetWriter
//                                                        ↓
//                                                     out.mp4
//
// Hardware H.264 / HEVC via VideoToolbox (AVAssetWriter picks automatically
// from the codec key). AAC audio at 128 kbps stereo.
//
// The recorder is controlled via a handle returned by kinrec_create; call
// kinrec_start to begin, kinrec_stop to finish writing + finalize the mp4.
// All exported functions are plain C ABI for purego consumption.
//
// Compile:
//   clang -dynamiclib -fobjc-arc -O2 -arch arm64 -arch x86_64 \
//     kinrec_writer.m \
//     -framework ScreenCaptureKit -framework AVFoundation \
//     -framework CoreMedia -framework CoreVideo \
//     -framework Foundation -framework CoreGraphics -framework AppKit \
//     -o libkinrec_writer.dylib

#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <AppKit/AppKit.h>

// NSApplicationLoad one-shot: Core Graphics window-scoped capture paths
// trip CGS_REQUIRE_INIT in CLI binaries that never connected to
// WindowServer. Same fix as sckit-go/docs/adr/003-cgs-init.md.
static void kinrec_ensure_cg_init(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{ NSApplicationLoad(); });
}

static void kinrec_copy_err(NSError* err, char* out, int cap) {
    if (!out || cap <= 0) return;
    NSString* s = err.localizedDescription ?: @"unknown error";
    const char* c = s.UTF8String ?: "nil";
    strncpy(out, c, cap - 1);
    out[cap - 1] = '\0';
}

static void kinrec_copy_str(const char* s, char* out, int cap) {
    if (!out || cap <= 0) return;
    strncpy(out, s, cap - 1);
    out[cap - 1] = '\0';
}

// ─── Audio mixer (system + mic → one track) ──────────────────
//
// Design: AVAudioEngine in offline manual-rendering mode. Two
// AVAudioPlayerNodes feed the main mixer. When a CMSampleBuffer arrives
// from either source, it is converted to the engine's canonical format
// (48 kHz stereo float32) via AVAudioConverter and scheduled on the
// matching player. A timer-driven render loop pulls 20 ms chunks from
// the mixer, converts them back to CMSampleBuffer, and forwards them
// via the onMixed callback — which the recorder uses to append to
// AVAssetWriterInput.
//
// Manual rendering avoids the engine's usual requirement of a live
// audio output device, which CLI binaries can't assume exists.

@interface KRAudioMixer : NSObject
@property (nonatomic, strong) AVAudioEngine* engine;
@property (nonatomic, strong) AVAudioPlayerNode* sysPlayer;
@property (nonatomic, strong) AVAudioPlayerNode* micPlayer;
@property (nonatomic, strong) AVAudioFormat* commonFormat;       // 48kHz stereo float32 non-interleaved
@property (nonatomic, strong) AVAudioFormat* renderFormat;       // same — manual-rendering output
@property (nonatomic, strong) AVAudioConverter* sysConverter;
@property (nonatomic, strong) AVAudioConverter* micConverter;
@property (nonatomic, copy) void (^onMixed)(CMSampleBufferRef);
@property (nonatomic, strong) dispatch_source_t renderTimer;
@property (nonatomic, assign) CMTime nextPTS;
@property (nonatomic, assign) BOOL hasAnchor;    // mixer clock anchored to host clock yet?
@property (nonatomic, assign) CMFormatDescriptionRef outputFmtDesc;  // for the emitted CMSampleBuffers
@property (nonatomic, assign) BOOL started;
@property (nonatomic, assign) BOOL stopped;
@end

@implementation KRAudioMixer

- (instancetype)init {
    if ((self = [super init])) {
        _commonFormat = [[AVAudioFormat alloc] initStandardFormatWithSampleRate:48000 channels:2];
        _renderFormat = _commonFormat;
        _nextPTS = kCMTimeInvalid;  // anchored on first input sample
        _hasAnchor = NO;
    }
    return self;
}

// Called on the first input CMSampleBuffer of ANY kind. Aligns the
// mixer's output timeline to the same host clock used by SCStream and
// AVCaptureSession — otherwise output PTS (starting at kCMTimeZero)
// would be decades apart from video PTS (mach absolute time) and
// players would render the video outside the timeline.
- (void)maybeAnchorAtPTS:(CMTime)pts {
    if (!CMTIME_IS_VALID(pts)) return;
    @synchronized(self) {
        if (!self.hasAnchor) {
            self.nextPTS = pts;
            self.hasAnchor = YES;
        }
    }
}

- (BOOL)setupWithError:(NSError**)outErr {
    self.engine = [[AVAudioEngine alloc] init];
    self.sysPlayer = [[AVAudioPlayerNode alloc] init];
    self.micPlayer = [[AVAudioPlayerNode alloc] init];

    [self.engine attachNode:self.sysPlayer];
    [self.engine attachNode:self.micPlayer];
    [self.engine connect:self.sysPlayer
                     to:self.engine.mainMixerNode
                 format:self.commonFormat];
    [self.engine connect:self.micPlayer
                     to:self.engine.mainMixerNode
                 format:self.commonFormat];

    // Manual rendering: engine does NOT touch any hardware output device.
    // We pull mixed output on our own timer.
    if (![self.engine enableManualRenderingMode:AVAudioEngineManualRenderingModeOffline
                                        format:self.renderFormat
                             maximumFrameCount:4096
                                         error:outErr]) {
        return NO;
    }

    // Lower the master gain so sum-of-two sources doesn't clip on peaks.
    self.engine.mainMixerNode.outputVolume = 0.9f;

    if (![self.engine startAndReturnError:outErr]) {
        return NO;
    }
    [self.sysPlayer play];
    [self.micPlayer play];

    // Build a CMAudioFormatDescription for the mixed CMSampleBuffers we
    // emit. AAC encoder accepts 48kHz stereo float32 non-interleaved.
    const AudioStreamBasicDescription* asbd = self.commonFormat.streamDescription;
    OSStatus st = CMAudioFormatDescriptionCreate(kCFAllocatorDefault,
                                                 asbd, 0, NULL, 0, NULL, NULL,
                                                 &_outputFmtDesc);
    if (st != 0) {
        if (outErr) *outErr = [NSError errorWithDomain:@"kinrec" code:20
                                              userInfo:@{NSLocalizedDescriptionKey:
                                                         [NSString stringWithFormat:@"CMAudioFormatDescriptionCreate failed (%d)", (int)st]}];
        return NO;
    }

    // Render timer — every 20 ms produces a 960-frame mixed chunk.
    dispatch_queue_t q = dispatch_queue_create("kinrec.mixer.render", DISPATCH_QUEUE_SERIAL);
    self.renderTimer = dispatch_source_create(DISPATCH_SOURCE_TYPE_TIMER, 0, 0, q);
    dispatch_source_set_timer(self.renderTimer,
                              dispatch_time(DISPATCH_TIME_NOW, 0),
                              20 * NSEC_PER_MSEC,
                              2 * NSEC_PER_MSEC);
    __weak KRAudioMixer* weakSelf = self;
    dispatch_source_set_event_handler(self.renderTimer, ^{
        [weakSelf renderTick];
    });
    return YES;
}

- (void)start {
    if (self.started) return;
    self.started = YES;
    dispatch_resume(self.renderTimer);
}

- (void)stop {
    if (self.stopped) return;
    self.stopped = YES;
    if (self.renderTimer) {
        dispatch_source_cancel(self.renderTimer);
        self.renderTimer = nil;
    }
    [self.sysPlayer stop];
    [self.micPlayer stop];
    [self.engine stop];
    if (_outputFmtDesc) {
        CFRelease(_outputFmtDesc);
        _outputFmtDesc = NULL;
    }
}

// Pull a 20 ms chunk from the engine, wrap it as a CMSampleBuffer, and
// hand it to the recorder.
- (void)renderTick {
    if (!self.started || self.stopped) return;
    if (!self.hasAnchor) return;  // no input yet — don't emit ghost PTS frames
    const AVAudioFrameCount framesToRender = 960; // 20 ms @ 48 kHz
    AVAudioPCMBuffer* out = [[AVAudioPCMBuffer alloc] initWithPCMFormat:self.renderFormat
                                                          frameCapacity:framesToRender];
    if (!out) return;

    NSError* err = nil;
    AVAudioEngineManualRenderingStatus status =
        [self.engine renderOffline:framesToRender toBuffer:out error:&err];
    if (status != AVAudioEngineManualRenderingStatusSuccess) return;
    if (out.frameLength == 0) return;

    CMSampleBufferRef sb = [self sampleBufferFromPCM:out];
    if (sb) {
        if (self.onMixed) self.onMixed(sb);
        CFRelease(sb);
    }
    // Advance output clock.
    self.nextPTS = CMTimeAdd(self.nextPTS,
                             CMTimeMake(out.frameLength, 48000));
}

// Convert an incoming CMSampleBuffer (from SCK or AVCaptureAudio) into
// an AVAudioPCMBuffer in the engine's common format, using a
// lazily-created AVAudioConverter bound to the input's format.
- (AVAudioPCMBuffer*)pcmBufferFromSample:(CMSampleBufferRef)sb
                            converterRef:(AVAudioConverter* __strong *)converterRef {
    CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sb);
    if (!fmt) return nil;
    const AudioStreamBasicDescription* asbd = CMAudioFormatDescriptionGetStreamBasicDescription(fmt);
    if (!asbd) return nil;
    AVAudioFormat* inputFmt = [[AVAudioFormat alloc] initWithStreamDescription:asbd];
    if (!inputFmt) return nil;

    if (*converterRef == nil ||
        ![(*converterRef).inputFormat isEqual:inputFmt]) {
        *converterRef = [[AVAudioConverter alloc] initFromFormat:inputFmt
                                                        toFormat:self.commonFormat];
        if (*converterRef == nil) return nil;
    }

    CMItemCount sampleCount = CMSampleBufferGetNumSamples(sb);
    if (sampleCount <= 0) return nil;

    // Build input PCM buffer from the sample buffer.
    AVAudioPCMBuffer* inputPCM = [[AVAudioPCMBuffer alloc] initWithPCMFormat:inputFmt
                                                              frameCapacity:(AVAudioFrameCount)sampleCount];
    if (!inputPCM) return nil;
    inputPCM.frameLength = (AVAudioFrameCount)sampleCount;

    // Copy the audio bytes from CMSampleBuffer into inputPCM.
    AudioBufferList abl;
    memset(&abl, 0, sizeof(abl));
    CMBlockBufferRef blockBuf = NULL;
    size_t neededSize = 0;
    OSStatus st = CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
        sb, &neededSize, NULL, 0, NULL, NULL,
        kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment, NULL);
    if (st != 0 || neededSize == 0) return nil;

    AudioBufferList* ablPtr = (AudioBufferList*)malloc(neededSize);
    st = CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
        sb, NULL, ablPtr, neededSize, NULL, NULL,
        kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment,
        &blockBuf);
    if (st != 0) {
        free(ablPtr);
        return nil;
    }

    // Copy bytes from ablPtr into inputPCM's mutableAudioBufferList.
    AudioBufferList* dst = inputPCM.mutableAudioBufferList;
    for (UInt32 i = 0; i < ablPtr->mNumberBuffers && i < dst->mNumberBuffers; i++) {
        UInt32 bytesToCopy = MIN(ablPtr->mBuffers[i].mDataByteSize,
                                 dst->mBuffers[i].mDataByteSize);
        memcpy(dst->mBuffers[i].mData, ablPtr->mBuffers[i].mData, bytesToCopy);
    }
    free(ablPtr);
    if (blockBuf) CFRelease(blockBuf);

    // Convert to common format.
    AVAudioFrameCount outputCapacity = (AVAudioFrameCount)(
        (double)sampleCount * self.commonFormat.sampleRate / inputFmt.sampleRate + 128);
    AVAudioPCMBuffer* outputPCM = [[AVAudioPCMBuffer alloc] initWithPCMFormat:self.commonFormat
                                                               frameCapacity:outputCapacity];
    if (!outputPCM) return nil;

    NSError* cvtErr = nil;
    __block BOOL delivered = NO;
    AVAudioConverterInputBlock inputBlock = ^AVAudioBuffer* (AVAudioPacketCount inNumberOfPackets,
                                                             AVAudioConverterInputStatus* outStatus) {
        if (delivered) {
            *outStatus = AVAudioConverterInputStatus_NoDataNow;
            return nil;
        }
        delivered = YES;
        *outStatus = AVAudioConverterInputStatus_HaveData;
        return inputPCM;
    };
    AVAudioConverterOutputStatus cStatus = [*converterRef convertToBuffer:outputPCM
                                                                     error:&cvtErr
                                                        withInputFromBlock:inputBlock];
    if (cStatus == AVAudioConverterOutputStatus_Error) return nil;
    return outputPCM;
}

- (void)appendSystemBuffer:(CMSampleBufferRef)sb {
    [self maybeAnchorAtPTS:CMSampleBufferGetPresentationTimeStamp(sb)];
    AVAudioPCMBuffer* pcm = [self pcmBufferFromSample:sb converterRef:&_sysConverter];
    if (!pcm || pcm.frameLength == 0) return;
    [self.sysPlayer scheduleBuffer:pcm atTime:nil options:0 completionHandler:nil];
}

- (void)appendMicBuffer:(CMSampleBufferRef)sb {
    [self maybeAnchorAtPTS:CMSampleBufferGetPresentationTimeStamp(sb)];
    AVAudioPCMBuffer* pcm = [self pcmBufferFromSample:sb converterRef:&_micConverter];
    if (!pcm || pcm.frameLength == 0) return;
    [self.micPlayer scheduleBuffer:pcm atTime:nil options:0 completionHandler:nil];
}

// Wrap the mixed AVAudioPCMBuffer back into a CMSampleBuffer the
// AVAssetWriterInput can swallow.
- (CMSampleBufferRef)sampleBufferFromPCM:(AVAudioPCMBuffer*)pcm {
    const AudioStreamBasicDescription* asbd = self.commonFormat.streamDescription;
    AudioBufferList* abl = pcm.mutableAudioBufferList;

    CMBlockBufferRef blockBuf = NULL;
    // Concatenate non-interleaved float32 channels into one contiguous
    // block buffer so AVAssetWriterInput sees a consistent view.
    // For non-interleaved: ABL has mNumberBuffers == channels, each with
    // frameLength * sizeof(float) bytes.
    size_t totalBytes = 0;
    for (UInt32 i = 0; i < abl->mNumberBuffers; i++) {
        totalBytes += abl->mBuffers[i].mDataByteSize;
    }
    if (totalBytes == 0) return NULL;

    void* concat = malloc(totalBytes);
    size_t off = 0;
    for (UInt32 i = 0; i < abl->mNumberBuffers; i++) {
        memcpy((uint8_t*)concat + off, abl->mBuffers[i].mData, abl->mBuffers[i].mDataByteSize);
        off += abl->mBuffers[i].mDataByteSize;
    }
    OSStatus st = CMBlockBufferCreateWithMemoryBlock(kCFAllocatorDefault,
                                                     concat, totalBytes,
                                                     kCFAllocatorMalloc, NULL,
                                                     0, totalBytes, 0, &blockBuf);
    if (st != 0) { free(concat); return NULL; }

    CMSampleBufferRef sb = NULL;
    CMSampleTimingInfo timing = {
        .duration = CMTimeMake(1, (int32_t)asbd->mSampleRate),
        .presentationTimeStamp = self.nextPTS,
        .decodeTimeStamp = kCMTimeInvalid,
    };
    st = CMSampleBufferCreate(kCFAllocatorDefault,
                              blockBuf, TRUE, NULL, NULL,
                              self.outputFmtDesc,
                              (CMItemCount)pcm.frameLength,
                              1, &timing,
                              0, NULL,
                              &sb);
    CFRelease(blockBuf);
    if (st != 0) return NULL;
    return sb;
}

@end

// ─── Recorder class ──────────────────────────────────────────

@interface KRRecorder : NSObject <SCStreamOutput, SCStreamDelegate,
                                   AVCaptureAudioDataOutputSampleBufferDelegate>
@property (nonatomic, strong) AVAssetWriter* writer;
@property (nonatomic, strong) AVAssetWriterInput* videoInput;
@property (nonatomic, strong) AVAssetWriterInput* audioInput;
@property (nonatomic, strong) SCStream* stream;
@property (nonatomic, strong) AVCaptureSession* micSession;
@property (nonatomic, strong) KRAudioMixer* mixer;  // non-nil only when mixing sys+mic

@property (nonatomic, assign) BOOL sessionStarted;
@property (nonatomic, assign) BOOL stopRequested;
@property (nonatomic, assign) int32_t width;
@property (nonatomic, assign) int32_t height;
@property (nonatomic, assign) int32_t fps;
@property (nonatomic, assign) BOOL wantsSystemAudio;
@property (nonatomic, assign) BOOL wantsMic;
@property (nonatomic, assign) CMTime startPTS;
@property (nonatomic, strong) NSURL* outputURL;
@property (nonatomic, assign) int64_t frameCount;
@property (nonatomic, assign) int64_t audioBufferCount;
@end

@implementation KRRecorder

- (BOOL)setupWriterWithPath:(NSString*)path
                       codec:(int)codec           // 0=h264, 1=hevc
                  fileType:(AVFileType)fileType
                       error:(NSError**)outErr {
    self.outputURL = [NSURL fileURLWithPath:path];
    // If the file already exists, AVAssetWriter will refuse — remove it.
    [[NSFileManager defaultManager] removeItemAtURL:self.outputURL error:nil];

    self.writer = [AVAssetWriter assetWriterWithURL:self.outputURL
                                           fileType:fileType
                                              error:outErr];
    if (!self.writer) return NO;

    // Video input. Bitrate is a heuristic: ~5 bits per pixel per second
    // at the configured fps. For 1920×1080 @ 60 = ~10 Mbps; at 30 = 5 Mbps.
    NSString* codecKey = (codec == 1) ? AVVideoCodecTypeHEVC : AVVideoCodecTypeH264;
    int64_t bitrate = (int64_t)self.width * self.height * 5 * self.fps / 30;
    if (bitrate < 1500000) bitrate = 1500000;

    NSDictionary* videoSettings = @{
        AVVideoCodecKey: codecKey,
        AVVideoWidthKey: @(self.width),
        AVVideoHeightKey: @(self.height),
        AVVideoCompressionPropertiesKey: @{
            AVVideoAverageBitRateKey: @(bitrate),
            AVVideoExpectedSourceFrameRateKey: @(self.fps),
            AVVideoMaxKeyFrameIntervalKey: @(self.fps * 2),
        },
    };
    self.videoInput = [AVAssetWriterInput assetWriterInputWithMediaType:AVMediaTypeVideo
                                                         outputSettings:videoSettings];
    self.videoInput.expectsMediaDataInRealTime = YES;
    if (![self.writer canAddInput:self.videoInput]) {
        if (outErr) *outErr = [NSError errorWithDomain:@"kinrec" code:1
                                              userInfo:@{NSLocalizedDescriptionKey: @"writer rejected video input"}];
        return NO;
    }
    [self.writer addInput:self.videoInput];

    // Audio track is added when EITHER system audio OR mic is requested.
    // Single AAC-LC mono or stereo track; source mic or system.
    if (self.wantsSystemAudio || self.wantsMic) {
        AudioChannelLayout stereoLayout = {0};
        stereoLayout.mChannelLayoutTag = kAudioChannelLayoutTag_Stereo;
        NSData* layoutData = [NSData dataWithBytes:&stereoLayout
                                            length:offsetof(AudioChannelLayout, mChannelDescriptions)];
        NSDictionary* audioSettings = @{
            AVFormatIDKey:          @(kAudioFormatMPEG4AAC),
            AVSampleRateKey:        @48000,
            AVNumberOfChannelsKey:  @2,
            AVChannelLayoutKey:     layoutData,
            AVEncoderBitRateKey:    @128000,
        };
        self.audioInput = [AVAssetWriterInput assetWriterInputWithMediaType:AVMediaTypeAudio
                                                             outputSettings:audioSettings];
        self.audioInput.expectsMediaDataInRealTime = YES;
        if (![self.writer canAddInput:self.audioInput]) {
            if (outErr) *outErr = [NSError errorWithDomain:@"kinrec" code:2
                                                  userInfo:@{NSLocalizedDescriptionKey: @"writer rejected audio input"}];
            return NO;
        }
        [self.writer addInput:self.audioInput];
    }
    return YES;
}

// setupMicWithError — attach a microphone input (default device) to an
// AVCaptureSession and wire its delegate to this recorder. The session
// is started by kinrec_start, stopped by kinrec_stop.
//
// Output format is forced to 48 kHz stereo Linear PCM so the
// AVAssetWriter's AAC encoder accepts it without a front-end converter.
- (BOOL)setupMicWithDeviceID:(NSString*)deviceID error:(NSError**)outErr {
    self.micSession = [[AVCaptureSession alloc] init];

    AVCaptureDevice* dev = nil;
    if (deviceID.length > 0) {
        dev = [AVCaptureDevice deviceWithUniqueID:deviceID];
    }
    if (!dev) {
        dev = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeAudio];
    }
    if (!dev) {
        if (outErr) *outErr = [NSError errorWithDomain:@"kinrec" code:10
                                              userInfo:@{NSLocalizedDescriptionKey: @"no microphone available"}];
        return NO;
    }

    AVCaptureDeviceInput* input = [AVCaptureDeviceInput deviceInputWithDevice:dev error:outErr];
    if (!input) return NO;
    if (![self.micSession canAddInput:input]) {
        if (outErr) *outErr = [NSError errorWithDomain:@"kinrec" code:11
                                              userInfo:@{NSLocalizedDescriptionKey: @"session rejected mic input"}];
        return NO;
    }
    [self.micSession addInput:input];

    AVCaptureAudioDataOutput* output = [[AVCaptureAudioDataOutput alloc] init];
    // Force a predictable PCM format the AAC encoder will accept.
    output.audioSettings = @{
        AVFormatIDKey:                @(kAudioFormatLinearPCM),
        AVSampleRateKey:              @48000,
        AVNumberOfChannelsKey:        @2,
        AVLinearPCMBitDepthKey:       @16,
        AVLinearPCMIsFloatKey:        @NO,
        AVLinearPCMIsBigEndianKey:    @NO,
        AVLinearPCMIsNonInterleaved:  @NO,
    };
    dispatch_queue_t q = dispatch_queue_create("kinrec.mic", DISPATCH_QUEUE_SERIAL);
    [output setSampleBufferDelegate:self queue:q];

    if (![self.micSession canAddOutput:output]) {
        if (outErr) *outErr = [NSError errorWithDomain:@"kinrec" code:12
                                              userInfo:@{NSLocalizedDescriptionKey: @"session rejected mic output"}];
        return NO;
    }
    [self.micSession addOutput:output];
    return YES;
}

// AVCaptureAudioDataOutputSampleBufferDelegate — mic audio arrives here.
- (void)captureOutput:(AVCaptureOutput*)captureOutput
    didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
    fromConnection:(AVCaptureConnection*)connection {
    (void)captureOutput; (void)connection;
    if (self.stopRequested || !self.audioInput) return;
    if (!CMSampleBufferIsValid(sampleBuffer)) return;

    // Mic may arrive before the first video frame. Start the writer
    // session on the FIRST sample buffer of any kind — same logic as
    // the SCStream path.
    @synchronized(self) {
        if (!self.sessionStarted) {
            CMTime pts = CMSampleBufferGetPresentationTimeStamp(sampleBuffer);
            if ([self.writer startWriting]) {
                [self.writer startSessionAtSourceTime:pts];
                self.startPTS = pts;
                self.sessionStarted = YES;
            } else {
                return;
            }
        }
    }

    // Route mic:
    //   - mixer present  → feed micPlayer
    //   - no mixer       → direct append (mic-only mode)
    if (self.mixer) {
        [self.mixer appendMicBuffer:sampleBuffer];
        self.audioBufferCount++;
    } else if (self.audioInput.readyForMoreMediaData) {
        [self.audioInput appendSampleBuffer:sampleBuffer];
        self.audioBufferCount++;
    }
}

// SCStream delivers video + audio sample buffers here. The sink must be
// re-entrant across the two sample-handler queues we registered.
- (void)stream:(SCStream*)stream
    didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
    ofType:(SCStreamOutputType)type {
    if (self.stopRequested) return;
    if (!CMSampleBufferIsValid(sampleBuffer)) return;

    if (type == SCStreamOutputTypeScreen) {
        // Filter idle / blank frames: only Complete and Started carry pixels.
        CFArrayRef attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, false);
        if (attachments && CFArrayGetCount(attachments) > 0) {
            CFDictionaryRef attach = (CFDictionaryRef)CFArrayGetValueAtIndex(attachments, 0);
            CFTypeRef statusRaw = CFDictionaryGetValue(attach,
                (__bridge CFStringRef)SCStreamFrameInfoStatus);
            if (statusRaw) {
                int statusVal = ((__bridge NSNumber*)statusRaw).intValue;
                if (statusVal != SCFrameStatusComplete && statusVal != SCFrameStatusStarted) {
                    return;
                }
            }
        }
    }

    // First content buffer starts the writer session. Lock to avoid the
    // video + audio queues racing on the first-sample path.
    @synchronized(self) {
        if (!self.sessionStarted) {
            CMTime pts = CMSampleBufferGetPresentationTimeStamp(sampleBuffer);
            if ([self.writer startWriting]) {
                [self.writer startSessionAtSourceTime:pts];
                self.startPTS = pts;
                self.sessionStarted = YES;
            } else {
                return;
            }
        }
    }

    if (type == SCStreamOutputTypeScreen) {
        if (self.videoInput.readyForMoreMediaData) {
            [self.videoInput appendSampleBuffer:sampleBuffer];
            self.frameCount++;
        }
    } else if (type == SCStreamOutputTypeAudio && self.audioInput && self.wantsSystemAudio) {
        // Route system audio:
        //   - mixer present  → feed sysPlayer for mixing with mic
        //   - no mixer       → direct append (sys-only mode)
        if (self.mixer) {
            [self.mixer appendSystemBuffer:sampleBuffer];
            self.audioBufferCount++;
        } else if (self.audioInput.readyForMoreMediaData) {
            [self.audioInput appendSampleBuffer:sampleBuffer];
            self.audioBufferCount++;
        }
    }
}

// SCStream delegate stop notification.
- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    // Intentionally empty — we stop via our own stopWithCompletion.
    (void)stream; (void)error;
}

- (void)stopWithCompletion:(void (^)(NSError* err))completion {
    if (self.stopRequested) {
        completion(nil);
        return;
    }
    self.stopRequested = YES;

    // Stop mic first (synchronous) to cut off further audio delivery.
    if (self.micSession) {
        [self.micSession stopRunning];
    }
    if (self.mixer) {
        [self.mixer stop];
        self.mixer = nil;
    }
    [self.stream stopCaptureWithCompletionHandler:^(NSError* stopErr) {
        (void)stopErr;
        [self.videoInput markAsFinished];
        if (self.audioInput) [self.audioInput markAsFinished];
        [self.writer finishWritingWithCompletionHandler:^{
            completion(self.writer.error);
        }];
    }];
}

@end

// ─── C ABI ───────────────────────────────────────────────────

typedef struct {
    void* recorder;   // KRRecorder* retained
} kinrec_handle_t;

// Create the recorder, wire it to SCStream, but DO NOT start capture yet.
// Call kinrec_start to begin. Call kinrec_stop to finalize.
//
// container: 0=mp4, 1=mov
// codec:     0=h264, 1=hevc
void* kinrec_create(uint32_t display_id,
                    int32_t  width,
                    int32_t  height,
                    int32_t  fps,
                    int32_t  capture_audio,
                    int32_t  capture_mic,
                    const char* mic_device_id,
                    int32_t  codec,
                    int32_t  container,
                    const char* output_path,
                    char* err_msg, int err_len) {
    kinrec_ensure_cg_init();
    if (!output_path || !*output_path) {
        kinrec_copy_str("output_path required", err_msg, err_len);
        return NULL;
    }

    __block SCDisplay* targetDisplay = nil;
    __block NSError* cap_err = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);

    [SCShareableContent getShareableContentWithCompletionHandler:
        ^(SCShareableContent* content, NSError* error) {
            if (error) { cap_err = error; dispatch_semaphore_signal(sem); return; }
            for (SCDisplay* d in content.displays) {
                if (d.displayID == display_id) { targetDisplay = d; break; }
            }
            if (!targetDisplay && content.displays.count > 0) {
                targetDisplay = content.displays.firstObject;
            }
            dispatch_semaphore_signal(sem);
        }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    if (cap_err || !targetDisplay) {
        if (cap_err) kinrec_copy_err(cap_err, err_msg, err_len);
        else kinrec_copy_str("display not found", err_msg, err_len);
        return NULL;
    }

    int32_t w = width  > 0 ? width  : (int32_t)targetDisplay.width;
    int32_t h = height > 0 ? height : (int32_t)targetDisplay.height;
    int32_t f = fps    > 0 ? fps    : 60;

    KRRecorder* rec = [[KRRecorder alloc] init];
    rec.width = w;
    rec.height = h;
    rec.fps = f;
    rec.wantsSystemAudio = (capture_audio != 0);
    rec.wantsMic = (capture_mic != 0);

    NSError* setupErr = nil;
    AVFileType fileType = (container == 1) ? AVFileTypeQuickTimeMovie : AVFileTypeMPEG4;
    NSString* path = [NSString stringWithUTF8String:output_path];
    if (![rec setupWriterWithPath:path codec:codec fileType:fileType error:&setupErr]) {
        if (setupErr) kinrec_copy_err(setupErr, err_msg, err_len);
        else kinrec_copy_str("writer setup failed", err_msg, err_len);
        return NULL;
    }

    // Build SCStream.
    SCContentFilter* filter = [[SCContentFilter alloc]
        initWithDisplay:targetDisplay excludingWindows:@[]];

    SCStreamConfiguration* config = [[SCStreamConfiguration alloc] init];
    config.width = w;
    config.height = h;
    config.pixelFormat = kCVPixelFormatType_32BGRA;
    config.showsCursor = YES;
    config.minimumFrameInterval = CMTimeMake(1, f);
    config.queueDepth = 5;
    if (rec.wantsSystemAudio) {
        config.capturesAudio = YES;
        config.excludesCurrentProcessAudio = YES;
        config.sampleRate = 48000;
        config.channelCount = 2;
    }

    SCStream* s = [[SCStream alloc] initWithFilter:filter
                                     configuration:config
                                          delegate:rec];
    NSError* addErr = nil;
    BOOL ok = [s addStreamOutput:rec
                            type:SCStreamOutputTypeScreen
              sampleHandlerQueue:dispatch_get_global_queue(QOS_CLASS_USER_INTERACTIVE, 0)
                           error:&addErr];
    if (!ok) {
        kinrec_copy_err(addErr, err_msg, err_len);
        return NULL;
    }
    if (rec.wantsSystemAudio) {
        ok = [s addStreamOutput:rec
                          type:SCStreamOutputTypeAudio
            sampleHandlerQueue:dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0)
                         error:&addErr];
        if (!ok) {
            kinrec_copy_err(addErr, err_msg, err_len);
            return NULL;
        }
    }
    rec.stream = s;

    if (rec.wantsMic) {
        NSString* mic_id = (mic_device_id && *mic_device_id)
            ? [NSString stringWithUTF8String:mic_device_id] : nil;
        NSError* micErr = nil;
        if (![rec setupMicWithDeviceID:mic_id error:&micErr]) {
            if (micErr) kinrec_copy_err(micErr, err_msg, err_len);
            else kinrec_copy_str("mic setup failed", err_msg, err_len);
            return NULL;
        }
    }

    // If BOTH system audio and mic are requested, build a mixer that
    // routes them through AVAudioEngine, produces a single mixed PCM
    // stream, and feeds that into the writer's audio input.
    if (rec.wantsSystemAudio && rec.wantsMic) {
        KRAudioMixer* mixer = [[KRAudioMixer alloc] init];
        NSError* mixErr = nil;
        if (![mixer setupWithError:&mixErr]) {
            if (mixErr) kinrec_copy_err(mixErr, err_msg, err_len);
            else kinrec_copy_str("mixer setup failed", err_msg, err_len);
            return NULL;
        }
        // Mixer delivers mixed CMSampleBuffers via this callback; the
        // recorder forwards to the writer's audio input.
        __weak KRRecorder* weakRec = rec;
        mixer.onMixed = ^(CMSampleBufferRef sb) {
            KRRecorder* strongRec = weakRec;
            if (!strongRec || strongRec.stopRequested) return;
            @synchronized(strongRec) {
                if (!strongRec.sessionStarted) {
                    CMTime pts = CMSampleBufferGetPresentationTimeStamp(sb);
                    if ([strongRec.writer startWriting]) {
                        [strongRec.writer startSessionAtSourceTime:pts];
                        strongRec.sessionStarted = YES;
                    } else {
                        return;
                    }
                }
            }
            if (strongRec.audioInput.readyForMoreMediaData) {
                [strongRec.audioInput appendSampleBuffer:sb];
            }
        };
        rec.mixer = mixer;
    }

    kinrec_handle_t* handle = calloc(1, sizeof(kinrec_handle_t));
    handle->recorder = (void*)CFBridgingRetain(rec);
    return handle;
}

// kinrec_start — begin capturing. Blocks until SCStream is running
// (~150ms) and, if mic is enabled, until AVCaptureSession is running.
int kinrec_start(void* handle_raw, char* err_msg, int err_len) {
    if (!handle_raw) { kinrec_copy_str("nil handle", err_msg, err_len); return -1; }
    kinrec_handle_t* h = (kinrec_handle_t*)handle_raw;
    KRRecorder* rec = (__bridge KRRecorder*)h->recorder;

    // Start the mic session first — captureSession startRunning is
    // synchronous. If the first audio buffer arrives before video we
    // want the writer's session-start logic to accept it.
    if (rec.micSession) {
        [rec.micSession startRunning];
    }
    if (rec.mixer) {
        [rec.mixer start];
    }

    __block NSError* cap_err = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [rec.stream startCaptureWithCompletionHandler:^(NSError* startErr) {
        if (startErr) cap_err = startErr;
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    if (cap_err) {
        // If video start failed, tear down mic as well.
        if (rec.micSession) [rec.micSession stopRunning];
        kinrec_copy_err(cap_err, err_msg, err_len);
        return -1;
    }
    return 0;
}

// kinrec_stop — flush + finalize mp4. Blocks until file is fully written.
int kinrec_stop(void* handle_raw, char* err_msg, int err_len) {
    if (!handle_raw) { kinrec_copy_str("nil handle", err_msg, err_len); return -1; }
    kinrec_handle_t* h = (kinrec_handle_t*)handle_raw;
    KRRecorder* rec = (__bridge KRRecorder*)h->recorder;

    __block NSError* cap_err = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [rec stopWithCompletion:^(NSError* err) {
        cap_err = err;
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    if (cap_err) {
        kinrec_copy_err(cap_err, err_msg, err_len);
        return -1;
    }
    return 0;
}

// kinrec_stats — export counters for progress display.
void kinrec_stats(void* handle_raw, int64_t* out_frames, int64_t* out_audio_buffers) {
    if (!handle_raw) return;
    kinrec_handle_t* h = (kinrec_handle_t*)handle_raw;
    KRRecorder* rec = (__bridge KRRecorder*)h->recorder;
    if (out_frames) *out_frames = rec.frameCount;
    if (out_audio_buffers) *out_audio_buffers = rec.audioBufferCount;
}

void kinrec_free(void* handle_raw) {
    if (!handle_raw) return;
    kinrec_handle_t* h = (kinrec_handle_t*)handle_raw;
    if (h->recorder) {
        CFBridgingRelease(h->recorder);
        h->recorder = NULL;
    }
    free(h);
}

// ─── List displays (convenience — kinrec can stand alone without sckit-go) ───

typedef struct {
    uint32_t display_id;
    int32_t  width;
    int32_t  height;
} kinrec_display_t;

// ─── List microphones ─────────────────────────────────────────
//
// Fixed-size struct + string pool, same layout pattern as sckit-go's
// window enumerator.
typedef struct {
    uint32_t is_default;     // 0 or 1 — default input
    uint32_t unique_id_offset;
    uint32_t unique_id_len;
    uint32_t name_offset;
    uint32_t name_len;
} kinrec_mic_t;

static BOOL kr_append_str(const char* s, char* pool, int pool_cap,
                          int* pool_used,
                          uint32_t* out_off, uint32_t* out_len) {
    if (!s) { *out_off = 0; *out_len = 0; return YES; }
    size_t slen = strlen(s);
    if (*pool_used + (int)slen > pool_cap) {
        *out_off = 0; *out_len = 0;
        return NO;
    }
    memcpy(pool + *pool_used, s, slen);
    *out_off = (uint32_t)*pool_used;
    *out_len = (uint32_t)slen;
    *pool_used += (int)slen;
    return YES;
}

int kinrec_list_mics(kinrec_mic_t* out_mics, int max_mics,
                     char* out_strings, int max_string_bytes,
                     int32_t* out_strings_used,
                     char* err_msg, int err_len) {
    int pool_used = 0;
    BOOL overflow = NO;

    // Collect all audio input devices. Prefer the modern
    // AVCaptureDeviceDiscoverySession on macOS 10.15+ so we see every
    // device (built-in, AirPods, USB mics, aggregate devices, ...).
    // macOS 14 renamed AVCaptureDeviceTypeExternalUnknown → AVCaptureDeviceTypeExternal.
    // The latter is correct on our minimum target (macOS 14+).
    // AVCaptureDeviceTypeMicrophone was added in macOS 14; on macOS 13
    // use the deprecated BuiltIn symbol. Our minimum target is macOS
    // 14, so use the new symbol and silence the warning explicitly.
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wunguarded-availability-new"
    NSArray<AVCaptureDeviceType>* deviceTypes = @[
        AVCaptureDeviceTypeMicrophone,
        AVCaptureDeviceTypeExternal,
    ];
#pragma clang diagnostic pop
    AVCaptureDeviceDiscoverySession* session =
        [AVCaptureDeviceDiscoverySession discoverySessionWithDeviceTypes:deviceTypes
                                                              mediaType:AVMediaTypeAudio
                                                               position:AVCaptureDevicePositionUnspecified];
    NSArray<AVCaptureDevice*>* devices = session.devices;
    AVCaptureDevice* defaultDev = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeAudio];

    int n = (int)devices.count;
    if (n > max_mics) n = max_mics;
    for (int i = 0; i < n; i++) {
        AVCaptureDevice* d = devices[i];
        out_mics[i].is_default = (d == defaultDev || [d.uniqueID isEqualToString:defaultDev.uniqueID]) ? 1 : 0;
        if (!kr_append_str(d.uniqueID.UTF8String ?: "", out_strings, max_string_bytes,
                           &pool_used,
                           &out_mics[i].unique_id_offset,
                           &out_mics[i].unique_id_len)) {
            overflow = YES; break;
        }
        if (!kr_append_str(d.localizedName.UTF8String ?: "", out_strings, max_string_bytes,
                           &pool_used,
                           &out_mics[i].name_offset,
                           &out_mics[i].name_len)) {
            overflow = YES; break;
        }
    }
    if (out_strings_used) *out_strings_used = (int32_t)pool_used;
    if (overflow) {
        kinrec_copy_str("string pool too small for mic list", err_msg, err_len);
        return -2;
    }
    return n;
}

int kinrec_list_displays(kinrec_display_t* out, int max, char* err_msg, int err_len) {
    __block int count = -1;
    __block NSError* cap_err = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [SCShareableContent getShareableContentWithCompletionHandler:
        ^(SCShareableContent* content, NSError* error) {
            if (error) { cap_err = error; dispatch_semaphore_signal(sem); return; }
            int n = (int)content.displays.count;
            if (n > max) n = max;
            for (int i = 0; i < n; i++) {
                SCDisplay* d = content.displays[i];
                out[i].display_id = d.displayID;
                out[i].width  = (int32_t)d.width;
                out[i].height = (int32_t)d.height;
            }
            count = n;
            dispatch_semaphore_signal(sem);
        }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    if (cap_err) kinrec_copy_err(cap_err, err_msg, err_len);
    return count;
}
