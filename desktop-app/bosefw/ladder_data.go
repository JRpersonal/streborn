package bosefw

// The firmware ladder tables. Phase 1: the Airplay-1 SoundTouch 20 / 30 /
// Portable on the older (scm) hardware, Bose device IDs 0x0923/24/25. Phase 2
// (further down): the SoundTouch 10, newer (sm2) hardware, device ID 0x0939.
//
// Every number below was checked by hand on 2026-10-26 against two sources and
// must stay that way: the Internet Archive item's metadata JSON
// (https://archive.org/metadata/bose-soundtouch-software-and-firmware, which
// publishes each zip's size and sha1) for ZipSize and ZipSHA1, and each zip's
// own central directory for InnerSize and InnerCRC32. InnerSHA1 was hashed from
// the extracted Update.stu where the zip was downloaded in full. 14.00.33 and
// the final 27.0.6 also match Bose's own catalogue (index.xml) byte for byte by
// size and CRC-32.
//
// No firmware byte is in this repository. The app links the Internet Archive's
// copy, and only the user's own PC fetches it, on the user's click, into the
// user's cache folder. The final step always comes from Bose's own host.
//
// LiveVerified is flipped only after a field report with a diagnostic bundle:
// so far one SoundTouch 20 Series I went 5.x -> 06.02.01 -> 07.00.37 ->
// 14.00.33 -> 16.00.09 by USB stick. 15.00.20 stalled on that speaker twice
// (2026-10-27): by stick and on the speaker's own update page, both with an
// empty bar. 16.00.09 then went in by stick from 14.00.33, so the ladder takes
// 16.00.09 in place of 15.00.20; it carries the same new installer generation
// (the format change at 15) and is the smallest file of that line. (A Portable
// went 27.0.3 -> 27.0.6 over the network, which says nothing about the hop from
// 16.00.09.) Every other step is unverified on real hardware.

// ArchiveItemBase is the only Internet Archive address a ladder file is
// fetched from.
const ArchiveItemBase = "https://archive.org/download/bose-soundtouch-software-and-firmware/"

// FamilySTScm is the phase-1 family: SoundTouch 20, 30 and Portable on the
// older (scm, no Bluetooth button on the 20 and 30) hardware.
const FamilySTScm = "st-scm"

const stScmFolder = "Firmware/2013-2015_No_Bluetooth/Airplay-1_ST20-30-Portable/"

func stScmArchive(version, zip string, zipSize int64, zipSHA1 string, innerSize int64, innerCRC uint32, innerSHA1, confidence string, live, fallback bool) Step {
	return archiveStep(FamilySTScm, stScmFolder, version, zip, zipSize, zipSHA1, innerSize, innerCRC, innerSHA1, confidence, live, fallback)
}

// archiveStep is one step of any ladder that comes from the Internet Archive
// item, out of the given folder.
func archiveStep(family, folder, version, zip string, zipSize int64, zipSHA1 string, innerSize int64, innerCRC uint32, innerSHA1, confidence string, live, fallback bool) Step {
	return Step{
		ID:           family + "-" + version,
		Version:      version,
		Family:       family,
		Source:       SourceArchive,
		URL:          ArchiveItemBase + folder + zip,
		FileName:     zip,
		ZipSize:      zipSize,
		ZipSHA1:      zipSHA1,
		InnerSize:    innerSize,
		InnerCRC32:   innerCRC,
		InnerSHA1:    innerSHA1,
		Confidence:   confidence,
		LiveVerified: live,
		Fallback:     fallback,
	}
}

// stScmLadder is the main ladder, oldest first. The last step is Bose's own
// file and is the only one the app may also send over the network.
var stScmLadder = []Step{
	stScmArchive("06.02.01", "Airplay-1_ST20-30-Portable_06.02.01.zip", 56867812, "70c0cb5b436c1b33fb9e52371f53f7b2b32b9fdb",
		80559847, 0xa70e1564, "20058e4632b866bf08f61593e09ab2823a36b886", "high", true, false),
	stScmArchive("07.00.37", "Airplay-1_ST20-30-Portable_07.00.37.zip", 65800764, "e2b2e7e0e23c7820e42f08e214c56a61c7ea20b4",
		93032539, 0xa46e94d0, "0586eddfe502d324ac2836998b2271b89eb927fd", "high", true, false),
	stScmArchive("14.00.33", "Airplay-1_ST20-30-Portable_14.00.33.zip", 67614484, "2d9898399328fab3bb79612a20bfdd42cac6dd60",
		93114711, 0xd4dc260b, "b3b563a4de548632a066f768edf92a1b35bd2095", "medium", true, false),
	// The inner sha1 is not known: the zip was not downloaded in full. The size
	// and CRC-32 come from its central directory, read on 2026-10-27.
	stScmArchive("16.00.09", "Airplay-1_ST20-30-Portable_Update_ti_16.00.09.37890.2412967.scm.stu.zip", 70289971, "e1366ffab91c785804bfc58e8318058be91c3e30",
		95712356, 0x62d60317, "", "medium-high", true, false),
	{
		ID:         FamilySTScm + "-27.0.6",
		Version:    "27.0.6",
		Family:     FamilySTScm,
		Source:     SourceBose,
		URL:        DownloadBase + "Update_ti_27.0.6.46330.5043500.scm.stu",
		FileName:   "Update_ti_27.0.6.46330.5043500.scm.stu",
		InnerSize:  105879988,
		InnerCRC32: 0x2d5a971e,
		Confidence: "high",
		// Bose's own file, flashed over the network on a real speaker, but
		// only from 27.0.3. The hop from 16.00.09 is not tried yet, so the
		// ladder does not call it tested.
		LiveVerified: false,
		NetworkOK:    true,
	},
}

// stScmFallback holds the smaller in-between steps the guide offers when a
// step leaves the speaker on its old version.
var stScmFallback = []Step{
	// Only for a speaker on 2.x or older that refuses 06.02.01.
	stScmArchive("05.00.23", "Airplay-1_ST20-30-Portable_05.00.23.zip", 54117996, "70b09185de8b027ff6bcdcd1f76374b48286e2b3",
		76821995, 0x3bae21ab, "107166e5cb7b2993376add269792f19e854a9227", "low", false, true),
	// Between 07.00.37 and 14.00.33 if that jump also stalls. There is no Bose
	// catalogue to check these two against, and no sha1 of the inner file.
	stScmArchive("09.00.41", "Airplay-1_ST20-30-Portable_09.00.41.zip", 67402612, "8854dd251aec11630367b0cbc629b0bbbdc27c80",
		92333191, 0xc4fd3a70, "", "low", false, true),
	stScmArchive("12.00.09", "Airplay-1_ST20-30-Portable_12.00.09.zip", 67590860, "c6e659517509c306a00d9774497d2b5d21742a7a",
		93083567, 0xc4d62d01, "", "low", false, true),
	// Between 16.x and 27.0.6 if that jump also stalls. On the one speaker
	// tried, every file above 100 MB stalled by stick and on the update page;
	// this is the largest one below it. Central directory read 2026-10-27.
	stScmArchive("20.00.06", "Airplay-1_ST20-30-Portable_Update_ti_20.00.06.44144.3075410.scm.stu.zip", 72549266, "80a4c22cb800eb5c2056457337de3b99b5960f46",
		98225732, 0x2f95d0db, "", "low", false, true),
}

// The SoundTouch 10 ladder (Bose device family "rhino", device ID 0x0939,
// only ever built on the newer sm2 hardware). Derived on 2026-11-22, every
// value from the same two kinds of source as the scm ladder: the archive
// item's metadata JSON (ZipSize, ZipSHA1) and each zip's central directory,
// read with HTTP range requests (InnerSize, InnerCRC32). Then each of the three
// zips went through the app's own download and extract path once
// (fetchArchiveZip, extractUpdateStu): sha1, size and CRC-32 all matched, and
// InnerSHA1 was hashed from the extracted Update.stu.
//
// Why these files:
//
//   - The archive's ST10 folder starts at 15.00.20: there is no rhino image
//     older than that. Bose's own catalogue for 14.0.33 (mr1_2017, kept in the
//     archive item as "Firmware/xml/index 14.0.33.xml", sha1 as in the item's
//     metadata) gives the SoundTouch 10 the SAME file as the Bluetooth
//     SoundTouch 20/30 and the Wireless Link adapter:
//     Update_ti_14.0.33.34225.2189603.sm2.stu, 79 892 339 bytes, CRC
//     b5300c87. The archive's ST20-30_or_Wireless_Link_Adapter 14.00.33 zip
//     holds exactly that Update.stu. So up to 14.0.33 the ST10 ran the shared
//     sm2 image, and that step is Bose's own file for it, byte for byte.
//   - From 15 on the ST10 has its own image. The archive's ST10 folder really
//     is the ST10's: its 27.0.6 holds 95 906 856 bytes / f18fe026, Bose's
//     current catalogue entry for 0x0939, and its 18.00.11 matches Bose's
//     archived 18.0.11 catalogue for 0x0939 (87 794 760 / f1536cc0, file
//     Update_ti_18.0.11.41145.2696371.rhino.sm2.stu). No catalogue of 15.0.20
//     survives (Bose answers 403 for mr2_2017-c7dabc12, the Wayback Machine
//     has no copy), so 15.00.20 is checked only by the archive's own values
//     and its place in that folder.
//   - A speaker below 14.0.33 takes 14.00.33 first, as on the scm ladder and
//     the rest of the Bluetooth chassis: the ladder lands each new installer
//     generation (new IPC at 14.00.33, new bootloader and kernel at 15) on a
//     system from the generation before it, never 9.x straight to 15.
//   - 16.00.09 is the fallback if 15.00.20 leaves the speaker on 14.x, the
//     same swap that worked on the scm ladder (15.00.20 stalled there twice,
//     16.00.09 went in). It is the smallest file of the rhino line.
//
// Nothing here is tried on a real SoundTouch 10 yet: every step stays
// LiveVerified false until a field report with a diagnostic bundle.

// FamilyST10Sm2 is the SoundTouch 10 ladder.
const FamilyST10Sm2 = "st10-sm2"

const (
	st10Folder = "Firmware/2015-2020_Bluetooth/Bluetooth_ST10/"
	sm2Folder  = "Firmware/2015-2020_Bluetooth/Bluetooth_ST20-30_or_Wireless_Link_Adapter/"
)

// st10Ladder is the SoundTouch 10's main ladder, oldest first. The last step
// is Bose's own file and is the only one the app may also send over the
// network.
var st10Ladder = []Step{
	// The shared sm2 image Bose's 14.0.33 catalogue gives the SoundTouch 10.
	archiveStep(FamilyST10Sm2, sm2Folder, "14.00.33", "Bluetooth_ST20-30_or_ST_WLA_Update_ti_14.00.33.34225.2189603.sm2.zip", 60560964, "0d70632bfd53b04253d9ca473dacad03bcbd71fe",
		79892339, 0xb5300c87, "ed8365e8daa97ee9c3a995c598d61fd76ccbfdd4", "medium", false, false),
	archiveStep(FamilyST10Sm2, st10Folder, "15.00.20", "Bluetooth_ST10_Update_ti_15.00.20.37272.2363762.rhino.sm2.zip", 71036950, "0fb7424457eccd8fcc5c0dc91f7993ff378ab850",
		91956244, 0x3f6c9be3, "505cc0066d41251e4c1efbdfa0cb24afaddc3464", "medium", false, false),
	{
		ID:         FamilyST10Sm2 + "-27.0.6",
		Version:    "27.0.6",
		Family:     FamilyST10Sm2,
		Source:     SourceBose,
		URL:        DownloadBase + "Update_ti_27.0.6.46330.5043500.rhino.sm2.stu",
		FileName:   "Update_ti_27.0.6.46330.5043500.rhino.sm2.stu",
		InnerSize:  95906856,
		InnerCRC32: 0xf18fe026,
		Confidence: "high",
		NetworkOK:  true,
	},
}

// st10Fallback holds the SoundTouch 10's alternative steps.
var st10Fallback = []Step{
	// In place of 15.00.20 if that one leaves the speaker on 14.x.
	archiveStep(FamilyST10Sm2, st10Folder, "16.00.09", "Bluetooth_ST10_Update_ti_16.00.09.37890.2412967.rhino.sm2.zip", 65986629, "2bff77ff551e82822ae18d227ce2c8d385bad7de",
		85550216, 0x8a52bda7, "5d881e824e541bacab55d2ed33d661d082359f52", "low", false, true),
}

// knownImage is an Update.stu from the archive item or one of Bose's
// catalogues whose model is known, and the ladder families that may carry it.
type knownImage struct {
	What     string
	Families []string // empty: no ladder may carry it yet
}

// knownImages are inner Update.stu CRC-32s whose model is known, taken from
// the same archive item and Bose's catalogues. The archive files a few images
// in the wrong folder (the Wave IV 14.00.33 is the SA-5's, several Wave and
// SA-4 files are the generic ST20/30 image), and the two hardware generations
// need different files, so a ladder must never carry another family's image.
// ladder_test.go checks it.
var knownImages = map[uint32]knownImage{
	0x45c71da9: {"Wave (scm) 06.02.01", nil},
	0xb10723ec: {"Wave (scm) 07.00.37", nil},
	0x69dce58a: {"Wave (scm) 14.00.33", nil},
	0x686cad33: {"Wave (scm) 15.00.20", nil},
	0x49d88de4: {"Wave (scm) 27.0.6", nil},
	0x2f0da602: {"SA-4 (scm) 14.0.33", nil},
	0xf6ef441d: {"SA-4 (scm) 15.0.20", nil},
	0x73f4cf4d: {"CineMate adapter (scm) 07.00.37", nil},
	0x05d309c2: {"CineMate adapter (scm) 14.00.33", nil},
	0x85f253e2: {"SA-5 (sm2) 14.00.33, also filed as Wave IV", nil},
	0x02bef2f1: {"SA-5 (sm2) 15.00.20", nil},
	0x7647c0be: {"SoundTouch 300 (sm2) 14.00.33", nil},
	0xb478ccf6: {"SoundTouch 300 (sm2) 15.00.20", nil},
	0xa2419a57: {"Wave IV (sm2) 14.0.33, Bose's catalogue only", nil},
	0x32a5024d: {"Wave IV (sm2) 15.00.20", nil},
	// Bose's 14.0.33 catalogue gives this one file to the SoundTouch 10, the
	// Bluetooth SoundTouch 20/30 and the Wireless Link adapter.
	0xb5300c87: {"ST20/30 Bluetooth, Wireless Link and SoundTouch 10 (sm2) 14.00.33", []string{FamilyST10Sm2}},
	0x56107f80: {"ST20/30 Bluetooth (sm2) 15.00.20", nil},
	0xafab1b5c: {"ST20/30 Bluetooth (sm2) 16.00.09", nil},
	0x536e6d6f: {"ST20/30 Bluetooth (sm2) 27.0.6", nil},
	0x3f6c9be3: {"SoundTouch 10 (sm2) 15.00.20", []string{FamilyST10Sm2}},
	0x8a52bda7: {"SoundTouch 10 (sm2) 16.00.09", []string{FamilyST10Sm2}},
	0xf18fe026: {"SoundTouch 10 (sm2) 27.0.6", []string{FamilyST10Sm2}},
}

// carriedBy reports whether a ladder of family may carry the image.
func (k knownImage) carriedBy(family string) bool {
	for _, f := range k.Families {
		if f == family {
			return true
		}
	}
	return false
}
