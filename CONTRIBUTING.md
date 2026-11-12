# Contributing

Thanks for helping improve LazaReplay.

## Report a problem

Use the bug-report form. Run `lazareplay-check.exe` first and paste its whole report: it lists your graphics chips and encoders and tests the recorder on your computer, which answers most of the questions a report raises.

Results from hardware the author does not have are especially useful: AMD graphics chips, NVIDIA desktop cards, Windows 11, screens in HDR or turned on their side. A passing self-check report is worth posting too.

**Do not attach a clip that shows anything private.** The self-check records a generated picture for exactly this reason.

For feature requests, describe what you were trying to record or save and what got in the way.

## Develop

Install Go 1.25 or newer. The repository has no third-party Go modules.

Tests that run on any platform:

```sh
go test ./internal/...
```

They cover the loop, the MP4 writer, the H.264 reader, the sound timeline, the pointer layers, the picture fitting and the settings. Format Go changes with `gofmt`.

On Windows, run `build.cmd` before submitting a change. It regenerates the resources, runs `go vet` and all the tests, and builds both programs. Then run the self-check, which is the test of everything that needs a graphics chip:

```powershell
.\dist\lazareplay-check.exe
```

The self-check has other modes that exercise one stage at a time. `lazareplay-check.exe record card=1 seconds=5 out=clip.mp4` followed by `lazareplay-check.exe verify file=clip.mp4 pointers=1` is the quickest loop while working on capture, conversion or encoding. Three environment variables help while developing:

| Variable | Effect |
| --- | --- |
| `LAZAREPLAY_PROFILE` | Any value makes `record` print where the capture thread spends its time |
| `LAZAREPLAY_FAULT` | A number makes a hardware encoder report a failure after that many pictures, to exercise the fallback to the next encoder |
| `LAZAREPLAY_SETTINGS` | The path of a settings file to use in place of the one in your application data folder, so that testing the app leaves your own settings alone |

`record` also takes `lose=90`, which makes the generated source fail every 90 pictures the way a monitor does when Windows takes it away, and `processor=1`, which converts with the chip's video processor in place of drawing.

### Layout

| Path | Contents |
| --- | --- |
| `internal/dx` | Direct3D: adapters and screens, desktop duplication, conversion, pointer drawing |
| `internal/mf` | Media Foundation: the H.264 and AAC encoders, and a reader used only to check clips |
| `internal/nvenc` | NVIDIA's encoder, driven directly |
| `internal/audio` | Loopback capture and the sound timeline |
| `internal/ring`, `internal/h264`, `internal/mp4` | The loop, the H.264 stream reader and the MP4 writer |
| `internal/recorder` | Ties the stages together; chooses the encoder; saves clips |
| `internal/testcard` | The picture the self-check records |
| `internal/winui`, `cmd/lazareplay` | The native Windows interface and the app |
| `cmd/lazareplay-check` | The self-check |
| `tools/mkres` | Draws the icon and builds the Windows resource object |

[How LazaReplay works](docs/HOW-IT-WORKS.md) explains the design.

### Rules for the recorder

- A frame is never copied to main memory. Only compressed data leaves the graphics chip. `Device.Read` exists for the self-check alone.
- The capture thread never waits for an encoder. It collects what is finished and hands over a picture only when the encoder is ready.
- Every structure that mirrors a Windows or driver structure says which one in its comment, and its layout is taken from the published header, not from memory.
- A pointer converted to `uintptr` for a call is converted in the argument list of that call, never stored first. `com.Call` and the encoder calls rely on this to keep the memory alive and in place.
- Recording is always visible. Do not add a way to run without the icon by the clock.
- Anything that changes what a clip contains needs a check in the self-check that would catch it being wrong.

To regenerate the icon and resources after changing the design, the manifest or `VERSION`:

```sh
go run ./tools/mkres
```

Commit the regenerated `assets/lazareplay.ico` and `cmd/lazareplay/resources_windows_amd64.syso`.

## Pull requests

Keep a pull request focused. Explain the problem, the resulting behaviour, and the checks performed, including the self-check report from your machine when the change touches recording.

## Releases

Update `VERSION`, `CHANGELOG.md`, and `docs/releases/v<version>.md` together, regenerate the resources, and push the matching `v<version>` tag. The release workflow builds on Windows, runs the tests, packages both programs, and publishes the ZIP and checksums after the Linux and Windows jobs pass.

See [the publishing guide](docs/GITHUB.md).
