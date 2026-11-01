plugins {
    `java-library`
}

description = "Platform-independent PandaEye plugin code: transport, codecs, batching, spool, clocks, labels"

dependencies {
    api(project(":protocol"))
    implementation(libs.aircompressor)
    // LiteBans is optional at run time (labels.litebans): compiled against a stub, provided by the server.
    compileOnly(project(":litebans-stub"))
    testImplementation(project(":litebans-stub"))
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
