package bosefw

// The firmware ladder tables. Phase 1: the Airplay-1 SoundTouch 20 / 30 /
// Portable on the older (scm) hardware, Bose device IDs 0x0923/24/25. Phase 2
// (further down): the SoundTouch 10, newer (sm2) hardware, device ID 0x0939.
// Phase 3 (at the end): the rest of the Bluetooth chassis on that hardware.
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

// The rest of the Bluetooth chassis (newer sm2 hardware): the SoundTouch 20
// and 30 with a Bluetooth button and the Wireless Link adapter (one shared
// image), the SA-5 ("burns"), the SoundTouch 300 ("ginger") and the Wave
// SoundTouch IV ("nelson"). Derived on 2026-11-22 the same way as the
// SoundTouch 10 ladder: ZipSize and ZipSHA1 from the archive item's metadata
// JSON, InnerSize and InnerCRC32 from each zip's central directory (HTTP range
// requests), InnerSHA1 from one run of each zip through the app's own download
// and extract path (fetchArchiveZip, extractUpdateStu), which matched sha1,
// size and CRC-32.
//
// Why these files:
//
//   - 14.00.33 is Bose's own file for each model, byte for byte: Bose's 14.0.33
//     catalogue ("Firmware/xml/index 14.0.33.xml" in the archive item) gives
//     the SoundTouch 20 and 30 with Bluetooth (0x093B, 0x093C) and the Wireless
//     Link adapter (0x094A) the shared Update_ti_14.0.33.34225.2189603.sm2.stu
//     (79 892 339 / b5300c87), the SA-5 (0x093A) the .burns.sm2.stu
//     (81 409 611 / 85f253e2) and the SoundTouch 300 (0x0949) the
//     .ginger.sm2.stu (82 612 595 / 7647c0be). The archive zips in each
//     model's folder hold exactly those.
//   - The Wave IV (0x093D) has no checked 14.00.33: Bose's file for it is the
//     .nelson.sm2.stu (86 543 011 / a2419a57), and the archive's Wave IV
//     14.00.33 zip holds the SA-5's file instead. So the Wave IV ladder starts
//     at 15.00.20, only for a speaker already on 14.0.33 or newer; below that
//     the plan says "no known source" (Plan).
//   - No Bose catalogue of 15.0.20 or 16.0.9 survives, so those steps are
//     checked by the archive's own values and their place in the model's
//     folder only: each zip's inner folder and file name name the model's
//     line (plain .sm2, .burns, .ginger, .nelson), the CRCs differ per model,
//     and the 27.0.6 in the same folder is Bose's current file for that model.
//   - 16.00.09 is the fallback if 15.00.20 leaves the speaker on 14.x, the
//     swap that worked on the scm ladder. The archive has no 16.00.09 for the
//     SoundTouch 300, so it has none.
//
// None of these steps has run on a real speaker yet: LiveVerified stays false
// until a field report with a diagnostic bundle.

// The other checked families of the newer hardware.
const (
	FamilyBTSm2    = "bt-sm2"    // SoundTouch 20/30 with Bluetooth, Wireless Link adapter
	FamilySA5Sm2   = "sa5-sm2"   // SA-5 amplifier
	FamilyST300Sm2 = "st300-sm2" // SoundTouch 300
	FamilyWaveSm2  = "wave-sm2"  // Wave SoundTouch IV
)

const (
	sa5Folder   = "Firmware/2015-2020_Bluetooth/Bluetooth_SA5/"
	st300Folder = "Firmware/2015-2020_Bluetooth/Bluetooth_ST300/"
	wave4Folder = "Firmware/2015-2020_Bluetooth/Bluetooth_Wave_SoundTouch_IV/"
)

// boseFinal is Bose's own 27.0.6 for a family: the file of the built-in
// catalogue (Builtin), and the only step that may also go over the network.
func boseFinal(family, file string, size int64, crc uint32) Step {
	return Step{
		ID:         family + "-" + LatestShort,
		Version:    LatestShort,
		Family:     family,
		Source:     SourceBose,
		URL:        DownloadBase + file,
		FileName:   file,
		InnerSize:  size,
		InnerCRC32: crc,
		Confidence: "high",
		NetworkOK:  true,
	}
}

var btSm2Ladder = []Step{
	archiveStep(FamilyBTSm2, sm2Folder, "14.00.33", "Bluetooth_ST20-30_or_ST_WLA_Update_ti_14.00.33.34225.2189603.sm2.zip", 60560964, "0d70632bfd53b04253d9ca473dacad03bcbd71fe",
		79892339, 0xb5300c87, "ed8365e8daa97ee9c3a995c598d61fd76ccbfdd4", "medium", false, false),
	archiveStep(FamilyBTSm2, sm2Folder, "15.00.20", "Bluetooth_ST20-30_or_ST_WLA_Update_ti_15.00.20.37272.2363762.sm2.zip", 73891694, "e94fee290f4597cbccee90314eec9e4f290d7a95",
		95495188, 0x56107f80, "0d2fc1debee883009d8b6eb15e6729e1a6e21a48", "medium", false, false),
	boseFinal(FamilyBTSm2, "Update_ti_27.0.6.46330.5043500.sm2.stu", 99445800, 0x536e6d6f),
}

var btSm2Fallback = []Step{
	archiveStep(FamilyBTSm2, sm2Folder, "16.00.09", "Bluetooth_ST20-30_or_ST_WLA_Update_ti_16.00.09.37890.2412967.sm2.zip", 68604564, "475063ca3c0a48b3d4c60b3b5045ae9e9f9b8e90",
		89089160, 0xafab1b5c, "d4d3d1abda779cf974e5d2e1996cc9b4e3c76e3d", "low", false, true),
}

var sa5Ladder = []Step{
	archiveStep(FamilySA5Sm2, sa5Folder, "14.00.33", "Bluetooth_SA5_14.00.33.zip", 61360424, "25c603ac10b3898db2b5ad6e8edffe4a4a3ac6b3",
		81409611, 0x85f253e2, "ac23c15af16b85490db8c010d63d40e3bf2f1ea0", "medium", false, false),
	archiveStep(FamilySA5Sm2, sa5Folder, "15.00.20", "Bluetooth_SA5_Update_ti_15.00.20.37272.2363762.burns.sm2.zip", 74690356, "534fa06d0942fddfbbe6621916d8c27d5089620c",
		97013524, 0x02bef2f1, "33dcf13bc463e214544142663354fd54bcef81fe", "medium", false, false),
	boseFinal(FamilySA5Sm2, "Update_ti_27.0.6.46330.5043500.burns.sm2.stu", 100957944, 0x0b99190b),
}

var sa5Fallback = []Step{
	archiveStep(FamilySA5Sm2, sa5Folder, "16.00.09", "Bluetooth_SA5_Update_ti_16.00.09.37890.2412967.burns.sm2.zip", 69382982, "3d97cfc98ec7b88b19123188ff83f8fe8b5b81f5",
		90575272, 0xe0f64638, "afd89183c1bf60d5b83457c94b598bf092ec8c3c", "low", false, true),
}

var st300Ladder = []Step{
	archiveStep(FamilyST300Sm2, st300Folder, "14.00.33", "Bluetooth_ST300_14.00.33.zip", 61648136, "e156049e4bcb25683e5540b429f63a3188d3b050",
		82612595, 0x7647c0be, "f2c0396257c6ae0ae69552a51f27c778b718df1c", "medium", false, false),
	archiveStep(FamilyST300Sm2, st300Folder, "15.00.20", "Bluetooth_ST300_Update_ti_15.00.20.37272.2363762.ginger.sm2.zip", 74972804, "aa4f7896cfb235ef6e001fb9a89b2707b1d5a011",
		98214156, 0xb478ccf6, "6bd6dd11e7ee099f4f02f3238aef597d1e0cb9ed", "medium", false, false),
	boseFinal(FamilyST300Sm2, "Update_ti_27.0.6.46330.5043500.ginger.sm2.stu", 102384736, 0xed21efd3),
}

// waveSm2Ladder has no 14.00.33: see above.
var waveSm2Ladder = []Step{
	archiveStep(FamilyWaveSm2, wave4Folder, "15.00.20", "Bluetooth_WST4_Update_ti_15.00.20.37272.2363762.nelson.sm2.zip", 74168336, "ee8e51fd2c6aad9246f8f74c393a96d3168c83c1",
		96583876, 0x32a5024d, "09efc54d585bc71de32926614957c496e41367a2", "medium", false, false),
	boseFinal(FamilyWaveSm2, "Update_ti_27.0.6.46330.5043500.nelson.sm2.stu", 100297132, 0x2e2fd417),
}

var waveSm2Fallback = []Step{
	archiveStep(FamilyWaveSm2, wave4Folder, "16.00.09", "Bluetooth_WST4_Update_ti_16.00.09.37890.2412967.nelson.sm2.zip", 69015749, "e776d0b1a8e9c2b0ee332be50cb196f13fa38995",
		90305140, 0x6e7d017c, "78e272cb2b5b33cb3882f28f0ac9e93217ad4e65", "low", false, true),
}

// waveSm2From is the oldest Wave IV firmware the checked ladder starts from.
const waveSm2From = "14.0.33"

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
	// The archive's Wave IV 14.00.33 holds this SA-5 file: only the SA-5
	// ladder may carry it, never the Wave IV one.
	0x85f253e2: {"SA-5 (sm2) 14.00.33, also filed as Wave IV", []string{FamilySA5Sm2}},
	0x02bef2f1: {"SA-5 (sm2) 15.00.20", []string{FamilySA5Sm2}},
	0xe0f64638: {"SA-5 (sm2) 16.00.09", []string{FamilySA5Sm2}},
	0x0b99190b: {"SA-5 (sm2) 27.0.6", []string{FamilySA5Sm2}},
	0x7647c0be: {"SoundTouch 300 (sm2) 14.00.33", []string{FamilyST300Sm2}},
	0xb478ccf6: {"SoundTouch 300 (sm2) 15.00.20", []string{FamilyST300Sm2}},
	0xed21efd3: {"SoundTouch 300 (sm2) 27.0.6", []string{FamilyST300Sm2}},
	0xa2419a57: {"Wave IV (sm2) 14.0.33, Bose's catalogue only", nil},
	0x32a5024d: {"Wave IV (sm2) 15.00.20", []string{FamilyWaveSm2}},
	0x6e7d017c: {"Wave IV (sm2) 16.00.09", []string{FamilyWaveSm2}},
	0x2e2fd417: {"Wave IV (sm2) 27.0.6", []string{FamilyWaveSm2}},
	// Bose's 14.0.33 catalogue gives this one file to the SoundTouch 10, the
	// Bluetooth SoundTouch 20/30 and the Wireless Link adapter.
	0xb5300c87: {"ST20/30 Bluetooth, Wireless Link and SoundTouch 10 (sm2) 14.00.33", []string{FamilyST10Sm2, FamilyBTSm2}},
	0x56107f80: {"ST20/30 Bluetooth and Wireless Link (sm2) 15.00.20", []string{FamilyBTSm2}},
	0xafab1b5c: {"ST20/30 Bluetooth and Wireless Link (sm2) 16.00.09", []string{FamilyBTSm2}},
	0x536e6d6f: {"ST20/30 Bluetooth and Wireless Link (sm2) 27.0.6", []string{FamilyBTSm2}},
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
