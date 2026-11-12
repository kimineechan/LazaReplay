# Using LazaReplay

## Start

Extract the ZIP anywhere and open `LazaReplay.exe`. There is nothing to install. Recording starts at once.

Windows may show a SmartScreen warning the first time, because the program is not signed. Choose **More info**, then **Run anyway**.

## 01 Replay

| Line | Meaning |
| --- | --- |
| Status | What is being recorded: which screen, the picture size and how many pictures a second. If something is wrong it says what, in words |
| Encoder | The video encoder in use, and whether it runs on the graphics chip or on the processor |
| In the loop | How much footage is held, out of the length you chose, and the memory it takes |
| Sound | Whether what the computer plays is being recorded, or why not |
| Last clip | The file saved most recently. **Show in folder** opens its folder with the file selected |

**Save the last 5 minutes** writes the loop to a new MP4 file. The loop is not emptied, so you can save again later and the clips will overlap.

**Pause recording** stops the recorder and wipes the loop from memory. Use it before doing anything you would not want in a clip. **Resume recording** starts an empty loop.

A clip can be a little longer than the length you chose, by up to two seconds. Video can only start from a keyframe, a picture that does not depend on earlier ones, and LazaReplay makes one every two seconds.

## Saving from anywhere

The shortcut, **Ctrl + Shift + F9** unless you change it, saves a clip whichever program is in front, including a game running full screen. If **Play a sound when a clip is saved** is ticked you hear the Windows notification sound when the file is written, which is the confirmation when the window is not on show.

Clips are named after the moment they were saved, such as `Replay 2026-11-12 14.03.22.mp4`.

## The icon by the clock

Closing the window does not stop LazaReplay. It keeps recording, and its icon stays in the notification area by the clock. Windows may tuck the icon into the overflow list, the small arrow next to the clock; drag it out to keep it in view.

- **Click** the icon to open the window.
- **Right-click** it for the menu: save a clip, open the window, open the clips folder, pause or resume, and **Exit**.

The icon loses its dot while recording is paused.

## 02 Settings

Changes take effect at once and are remembered. Changing the length, the quality, the picture, the screen or the sound starts the loop afresh, so save first if there is something in it you want.

| Setting | What it does |
| --- | --- |
| Keep the last | The length of the loop: 30 seconds, 1, 2, 5 or 10 minutes. The line beside it shows the most memory the loop will take |
| Quality | Low, Medium or High. Higher quality is a higher bitrate, which is more memory and larger files. The line beside it shows the picture size and bitrate that result |
| Picture | The tallest picture to record. A larger screen is scaled down to it; a smaller one is never scaled up. Then the rate: 30 or 60 pictures a second |
| Screen | Which screen to record, when there is more than one |
| Shortcut | Click the box and press the keys you want. A key on its own, or with Shift alone, has Ctrl and Alt added to it, because otherwise it would be taken away from every other program |
| Record what the computer plays | Sound from the default playback device. Untick for silent clips |
| Save clips to | The folder for clips. **Change** picks another |
| Play a sound when a clip is saved | The confirmation described above |
| Start with Windows | Starts LazaReplay when you sign in, with no window, recording from the icon by the clock |

### How much memory

The loop holds compressed video, so its size is the bitrate multiplied by the length. These are the largest sizes for a 1920 by 1080 picture at 60 a second with sound, reached only when the picture is busy all the time. A quiet desktop uses a small fraction.

| Length | Low | Medium | High |
| --- | --- | --- | --- |
| 30 seconds | 18 MB | 36 MB | 65 MB |
| 1 minute | 37 MB | 73 MB | 130 MB |
| 2 minutes | 74 MB | 145 MB | 260 MB |
| 5 minutes | 185 MB | 363 MB | 649 MB |
| 10 minutes | 369 MB | 727 MB | 1.3 GB |

At 30 pictures a second the sizes are about six tenths of these.

## Starting and stopping from a script

LazaReplay runs one copy at a time. Starting it again talks to the copy that is running.

| Command | Effect |
| --- | --- |
| `LazaReplay.exe` | Starts it, or brings the running copy's window to the front |
| `LazaReplay.exe --tray` | Starts it with no window |
| `LazaReplay.exe --save` | Tells the running copy to save a clip |
| `LazaReplay.exe --exit` | Tells the running copy to close. A clip that is being written is finished first |

`--save` lets a macro key, a stream deck or a second shortcut tool save clips.

## The self-check

`lazareplay-check.exe` tests the recorder on your computer. Double-click it, or run it in a terminal. It takes under a minute and prints a report:

1. The graphics chips, the screens on each, and the video encoders each offers.
2. For every screen, a recording of a generated test picture made through that screen's chip. The saved clip is played back with the decoders built into Windows and compared with the picture by numbers: every frame present and in order, the colours, the mouse pointer, the time between pictures, and sound in step with the picture.
3. For every screen, three seconds of the real screen and sound, to see that capture works. Nothing on the screen is examined, and the clip is deleted.
4. The software encoder, which is the fallback when no hardware encoder works.

Each check ends in `ok` or `FAILED`. Paste the whole report into a bug report: it says exactly which encoder was used and what went wrong.

## Status messages

| Message | What it means |
| --- | --- |
| Starting... | The recorder is being set up. This takes under a second |
| Windows is showing a protected screen. Recording will resume by itself. | The lock screen or an administrator prompt is up. Windows does not let programs see those |
| The graphics driver restarted. Recording will resume by itself. | A driver update or reset. The loop carries on from where capture resumes |
| This screen cannot be captured, for example over Remote Desktop. | Windows does not offer this screen for duplication |
| Too many programs are capturing the screen at once. | Windows limits how many programs can duplicate one screen. Close another recorder |
| No video encoder could be started | Neither a hardware encoder nor the Windows software encoder would start. The message lists why. Run the self-check |
| No playback device was found, so clips will be silent. | There is nothing to record sound from |
| The media features of Windows are missing. | An N or KN edition of Windows without the Media Feature Pack. Add it under Settings, Apps, Optional features |
| *Shortcut* is taken by another program. | Another program registered the same keys first. Choose a different shortcut in Settings |
| There is not enough room on that disk | The clip was not saved. Free some space or change the folder, then save again; the loop is still in memory |

## Where things are kept

| What | Where |
| --- | --- |
| Clips | The `LazaReplay` folder in your Videos folder, unless you chose another |
| Settings | `%APPDATA%\LazaReplay\settings.json` |
| Start with Windows | An entry named LazaReplay in the per-user start-up list, which Task Manager shows under Start-up apps |

To remove LazaReplay, untick **Start with Windows**, exit it, and delete the program and the settings folder.

## Games

LazaReplay records the screen the way Windows composes it, and does not attach itself to the game, so it works with games that do not allow overlays. Two things are worth knowing.

- Record the screen the game is on. On a laptop, the built-in screen is usually driven by the processor's graphics chip even when the game runs on the other one; that is the best case, because recording then uses a chip the game is not using.
- If the picture size is above 1080p, 1080p at 60 a second is the setting that costs least for a result that still looks sharp.
