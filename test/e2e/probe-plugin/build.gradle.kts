// PandaEye e2e probe plugin: measures what really goes on the wire (netty taps), PacketEvents
// LOWEST/MONITOR event state, kicks and TPS. Test-only; never shipped.
plugins {
    java
}

group = "dev.pandascript.pandaeye.e2e"
version = "0.1.0"

java {
    toolchain { languageVersion.set(JavaLanguageVersion.of(21)) }
}

repositories {
    mavenCentral()
    maven("https://repo.papermc.io/repository/maven-public/")
    maven("https://repo.codemc.io/repository/maven-releases/")
    maven("https://repo.codemc.io/repository/maven-snapshots/")
}

dependencies {
    compileOnly("io.papermc.paper:paper-api:1.21.4-R0.1-SNAPSHOT")
    compileOnly("com.github.retrooper:packetevents-spigot:2.13.0")
    // Paper 1.21.11 ships netty 4.2.7; only stable 4.1/4.2 API (ChannelHandler adapters, ByteBuf) is used.
    compileOnly("io.netty:netty-transport:4.2.7.Final")
    compileOnly("io.netty:netty-buffer:4.2.7.Final")
}

tasks.withType<JavaCompile>().configureEach {
    options.release.set(21)
    options.encoding = "UTF-8"
    options.compilerArgs.add("-Xlint:deprecation")
}

tasks.processResources {
    filesMatching("plugin.yml") { expand("version" to project.version) }
}

tasks.jar {
    archiveFileName.set("PandaEyeProbe.jar")
}
