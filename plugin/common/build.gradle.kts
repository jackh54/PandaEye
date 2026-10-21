plugins {
    `java-library`
}

description = "Platform-independent PandaEye plugin code: transport, codecs, batching, spool, clocks"

dependencies {
    api(project(":protocol"))
    implementation(libs.aircompressor)
}

val fixturesDir = rootProject.layout.projectDirectory.dir("../test/fixtures")

tasks.test {
    systemProperty("pandaeye.fixtures", fixturesDir.asFile.absolutePath)
    inputs.dir(fixturesDir).withPropertyName("fixtures").optional()
}

tasks.register<JavaExec>("writeFixtures") {
    group = "verification"
    description = "Regenerates the cross-language codec fixtures in test/fixtures"
    classpath = sourceSets.test.get().runtimeClasspath
    mainClass.set("dev.pandascript.pandaeye.common.codec.FixtureWriter")
    args(fixturesDir.asFile.absolutePath)
}
