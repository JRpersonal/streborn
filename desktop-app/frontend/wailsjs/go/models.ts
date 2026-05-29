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
	    }
	}
	export class InstallResult {
	    step: string;
	    ok: boolean;
	    message: string;
	    log: string;
	
	    static createFrom(source: any = {}) {
	        return new InstallResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.step = source["step"];
	        this.ok = source["ok"];
	        this.message = source["message"];
	        this.log = source["log"];
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
	export class StickConfigs {
	    wlanSSID: string;
	    wlanPass: string;
	    region: string;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new StickConfigs(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.wlanSSID = source["wlanSSID"];
	        this.wlanPass = source["wlanPass"];
	        this.region = source["region"];
	        this.name = source["name"];
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

