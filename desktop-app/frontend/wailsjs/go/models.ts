export namespace main {
	
	export class AppInfo {
	    version: string;
	    build: string;
	    author: string;
	    githubUrl: string;
	    websiteUrl: string;
	    donateUrl: string;
	    donateSlogan: string;
	    updateManifestUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.build = source["build"];
	        this.author = source["author"];
	        this.githubUrl = source["githubUrl"];
	        this.websiteUrl = source["websiteUrl"];
	        this.donateUrl = source["donateUrl"];
	        this.donateSlogan = source["donateSlogan"];
	        this.updateManifestUrl = source["updateManifestUrl"];
	    }
	}
	export class BoxInfo {
	    name: string;
	    host: string;
	    port: number;
	    deviceID: string;
	    friendlyName: string;
	    model: string;
	    version: string;
	    build: string;
	    serialNumber: string;
	    kind: string;
	    portVerified: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BoxInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.host = source["host"];
	        this.port = source["port"];
	        this.deviceID = source["deviceID"];
	        this.friendlyName = source["friendlyName"];
	        this.model = source["model"];
	        this.version = source["version"];
	        this.build = source["build"];
	        this.serialNumber = source["serialNumber"];
	        this.kind = source["kind"];
	        this.portVerified = source["portVerified"];
	    }
	}
	export class BoxPresetInfo {
	    slot: number;
	    source: string;
	    type: string;
	    location: string;
	    sourceAccount: string;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new BoxPresetInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slot = source["slot"];
	        this.source = source["source"];
	        this.type = source["type"];
	        this.location = source["location"];
	        this.sourceAccount = source["sourceAccount"];
	        this.name = source["name"];
	    }
	}
	export class FirmwareInfo {
	    reachable: boolean;
	    model: string;
	    firmware: string;
	    short: string;
	    moduleType: string;
	    variant: string;
	    latest: string;
	    outdated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FirmwareInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.reachable = source["reachable"];
	        this.model = source["model"];
	        this.firmware = source["firmware"];
	        this.short = source["short"];
	        this.moduleType = source["moduleType"];
	        this.variant = source["variant"];
	        this.latest = source["latest"];
	        this.outdated = source["outdated"];
	    }
	}
	export class InstallResult {
	    step: string;
	    code: string;
	    ok: boolean;
	    message: string;
	    log: string;
	    firmware: string;
	
	    static createFrom(source: any = {}) {
	        return new InstallResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.step = source["step"];
	        this.code = source["code"];
	        this.ok = source["ok"];
	        this.message = source["message"];
	        this.log = source["log"];
	        this.firmware = source["firmware"];
	    }
	}
	export class LibraryContainer {
	    id: string;
	    parentID: string;
	    title: string;
	    childCount: number;
	
	    static createFrom(source: any = {}) {
	        return new LibraryContainer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.parentID = source["parentID"];
	        this.title = source["title"];
	        this.childCount = source["childCount"];
	    }
	}
	export class LibraryItem {
	    id: string;
	    parentID: string;
	    title: string;
	    artist: string;
	    album: string;
	    mimeType: string;
	    streamURL: string;
	    albumArtURL: string;
	    durationSec: number;
	
	    static createFrom(source: any = {}) {
	        return new LibraryItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.parentID = source["parentID"];
	        this.title = source["title"];
	        this.artist = source["artist"];
	        this.album = source["album"];
	        this.mimeType = source["mimeType"];
	        this.streamURL = source["streamURL"];
	        this.albumArtURL = source["albumArtURL"];
	        this.durationSec = source["durationSec"];
	    }
	}
	export class LibraryPage {
	    containers: LibraryContainer[];
	    items: LibraryItem[];
	    totalMatches: number;
	    returned: number;
	
	    static createFrom(source: any = {}) {
	        return new LibraryPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.containers = this.convertValues(source["containers"], LibraryContainer);
	        this.items = this.convertValues(source["items"], LibraryItem);
	        this.totalMatches = source["totalMatches"];
	        this.returned = source["returned"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LibraryServer {
	    udn: string;
	    friendlyName: string;
	    manufacturer: string;
	    modelName: string;
	    iconURL: string;
	    address: string;
	
	    static createFrom(source: any = {}) {
	        return new LibraryServer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.udn = source["udn"];
	        this.friendlyName = source["friendlyName"];
	        this.manufacturer = source["manufacturer"];
	        this.modelName = source["modelName"];
	        this.iconURL = source["iconURL"];
	        this.address = source["address"];
	    }
	}
	export class LogExportRequest {
	    savePath: string;
	    boxHosts: string[];
	    anonymize: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LogExportRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.savePath = source["savePath"];
	        this.boxHosts = source["boxHosts"];
	        this.anonymize = source["anonymize"];
	    }
	}
	export class LogExportResult {
	    savePath: string;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new LogExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.savePath = source["savePath"];
	        this.bytes = source["bytes"];
	    }
	}
	export class Preset {
	    slot: number;
	    name: string;
	    stream_url: string;
	    type: string;
	    art?: string;
	    bitrate?: number;
	    uri?: string;
	    account?: string;
	    source?: string;
	    homepage?: string;
	
	    static createFrom(source: any = {}) {
	        return new Preset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slot = source["slot"];
	        this.name = source["name"];
	        this.stream_url = source["stream_url"];
	        this.type = source["type"];
	        this.art = source["art"];
	        this.bitrate = source["bitrate"];
	        this.uri = source["uri"];
	        this.account = source["account"];
	        this.source = source["source"];
	        this.homepage = source["homepage"];
	    }
	}
	export class RadioSearchOpts {
	    q: string;
	    cc: string;
	    lang: string;
	    tag: string;
	    order: string;
	    limit: number;
	    offset: number;
	    onlyok: boolean;
	    top: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RadioSearchOpts(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.q = source["q"];
	        this.cc = source["cc"];
	        this.lang = source["lang"];
	        this.tag = source["tag"];
	        this.order = source["order"];
	        this.limit = source["limit"];
	        this.offset = source["offset"];
	        this.onlyok = source["onlyok"];
	        this.top = source["top"];
	    }
	}
	export class RecentItem {
	    ts: string;
	    source: string;
	    cardKey: string;
	    cardName: string;
	    cardArt: string;
	    cardURL: string;
	    track: string;
	    account: string;
	    homepage: string;
	
	    static createFrom(source: any = {}) {
	        return new RecentItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ts = source["ts"];
	        this.source = source["source"];
	        this.cardKey = source["cardKey"];
	        this.cardName = source["cardName"];
	        this.cardArt = source["cardArt"];
	        this.cardURL = source["cardURL"];
	        this.track = source["track"];
	        this.account = source["account"];
	        this.homepage = source["homepage"];
	    }
	}
	export class SetupAPPushResult {
	    step: string;
	    message: string;
	    ok: boolean;
	    logTail: string[];
	
	    static createFrom(source: any = {}) {
	        return new SetupAPPushResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.step = source["step"];
	        this.message = source["message"];
	        this.ok = source["ok"];
	        this.logTail = source["logTail"];
	    }
	}
	export class SpotifyNow {
	    bitrate: number;
	    track: string;
	    artist: string;
	    cover: string;
	    context: string;
	    account: string;
	    premiumRequired: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SpotifyNow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bitrate = source["bitrate"];
	        this.track = source["track"];
	        this.artist = source["artist"];
	        this.cover = source["cover"];
	        this.context = source["context"];
	        this.account = source["account"];
	        this.premiumRequired = source["premiumRequired"];
	    }
	}
	export class TrueFactoryResetResult {
	    step: string;
	    ok: boolean;
	    message: string;
	    log: string;
	    wipedFiles: string[];
	
	    static createFrom(source: any = {}) {
	        return new TrueFactoryResetResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.step = source["step"];
	        this.ok = source["ok"];
	        this.message = source["message"];
	        this.log = source["log"];
	        this.wipedFiles = source["wipedFiles"];
	    }
	}
	export class UninstallSTRResult {
	    step: string;
	    ok: boolean;
	    stickPresent: boolean;
	    message: string;
	    log: string;
	    removedFiles: string[];
	
	    static createFrom(source: any = {}) {
	        return new UninstallSTRResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.step = source["step"];
	        this.ok = source["ok"];
	        this.stickPresent = source["stickPresent"];
	        this.message = source["message"];
	        this.log = source["log"];
	        this.removedFiles = source["removedFiles"];
	    }
	}
	export class UpdateAsset {
	    version: string;
	    sha256: string;
	    url: string;
	    filename: string;
	    autoInstall: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UpdateAsset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.sha256 = source["sha256"];
	        this.url = source["url"];
	        this.filename = source["filename"];
	        this.autoInstall = source["autoInstall"];
	    }
	}
	export class ZoneMember {
	    deviceID: string;
	    ip: string;
	
	    static createFrom(source: any = {}) {
	        return new ZoneMember(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceID = source["deviceID"];
	        this.ip = source["ip"];
	    }
	}
	export class ZoneSpec {
	    master: ZoneMember;
	    slaves: ZoneMember[];
	    name: string;
	    stereo: boolean;
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new ZoneSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.master = this.convertValues(source["master"], ZoneMember);
	        this.slaves = this.convertValues(source["slaves"], ZoneMember);
	        this.name = source["name"];
	        this.stereo = source["stereo"];
	        this.mode = source["mode"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace radiobrowser {
	
	export class Language {
	    name: string;
	    iso_639: string;
	    stationcount: number;
	
	    static createFrom(source: any = {}) {
	        return new Language(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.iso_639 = source["iso_639"];
	        this.stationcount = source["stationcount"];
	    }
	}
	export class Station {
	    stationuuid: string;
	    name: string;
	    url: string;
	    url_resolved: string;
	    favicon: string;
	    homepage: string;
	    tags: string;
	    country: string;
	    countrycode: string;
	    language: string;
	    state: string;
	    codec: string;
	    bitrate: number;
	    hls: number;
	    votes: number;
	    clickcount: number;
	    clicktrend: number;
	    lastcheckok: number;
	
	    static createFrom(source: any = {}) {
	        return new Station(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.stationuuid = source["stationuuid"];
	        this.name = source["name"];
	        this.url = source["url"];
	        this.url_resolved = source["url_resolved"];
	        this.favicon = source["favicon"];
	        this.homepage = source["homepage"];
	        this.tags = source["tags"];
	        this.country = source["country"];
	        this.countrycode = source["countrycode"];
	        this.language = source["language"];
	        this.state = source["state"];
	        this.codec = source["codec"];
	        this.bitrate = source["bitrate"];
	        this.hls = source["hls"];
	        this.votes = source["votes"];
	        this.clickcount = source["clickcount"];
	        this.clicktrend = source["clicktrend"];
	        this.lastcheckok = source["lastcheckok"];
	    }
	}
	export class Tag {
	    name: string;
	    stationcount: number;
	
	    static createFrom(source: any = {}) {
	        return new Tag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.stationcount = source["stationcount"];
	    }
	}

}

export namespace sticksetup {
	
	export class Drive {
	    path: string;
	    label: string;
	    totalBytes: number;
	    freeBytes: number;
	    filesystem: string;
	    removable: boolean;
	    hasStick: boolean;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new Drive(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.label = source["label"];
	        this.totalBytes = source["totalBytes"];
	        this.freeBytes = source["freeBytes"];
	        this.filesystem = source["filesystem"];
	        this.removable = source["removable"];
	        this.hasStick = source["hasStick"];
	        this.description = source["description"];
	    }
	}
	export class StickCheck {
	    ok: boolean;
	    path: string;
	    filesystem: string;
	    totalBytes: number;
	    isFat32: boolean;
	    bigEnough: boolean;
	    writable: boolean;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new StickCheck(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.path = source["path"];
	        this.filesystem = source["filesystem"];
	        this.totalBytes = source["totalBytes"];
	        this.isFat32 = source["isFat32"];
	        this.bigEnough = source["bigEnough"];
	        this.writable = source["writable"];
	        this.reason = source["reason"];
	    }
	}
	export class StickConfigs {
	    wlanSSID: string;
	    wlanPass: string;
	    region: string;
	    name: string;
	    locale: string;
	
	    static createFrom(source: any = {}) {
	        return new StickConfigs(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.wlanSSID = source["wlanSSID"];
	        this.wlanPass = source["wlanPass"];
	        this.region = source["region"];
	        this.name = source["name"];
	        this.locale = source["locale"];
	    }
	}

}

export namespace wifiprofiles {
	
	export class Profile {
	    ssid: string;
	    hasPass: boolean;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new Profile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ssid = source["ssid"];
	        this.hasPass = source["hasPass"];
	        this.source = source["source"];
	    }
	}

}

