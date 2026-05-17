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

