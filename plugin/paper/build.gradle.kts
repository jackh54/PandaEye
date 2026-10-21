plugins {
    java
    alias(libs.plugins.shadow)
}

description = "PandaEye plugin for Paper and Folia"

dependencies {
    implementation(project(":common"))
    compileOnly(libs.paper.api)
    compileOnly(libs.packetevents.spigot)

    testImplementation(libs.paper.api)
    testImplementation(libs.packetevents.spigot)
    testImplementation(libs.netty.buffer)
    testImplementation(libs.archunit)
}

val checksDoc = rootProject.layout.projectDirectory.file("../docs/CHECKS.md")

tasks.test {
    systemProperty("pandaeye.checksDoc", checksDoc.asFile.absolutePath)
    inputs.file(checksDoc).withPropertyName("checksDoc")
}

tasks.processResources {
    val version = project.version.toString()
    inputs.property("version", version)
    filesMatching("plugin.yml") {
        expand("version" to version)
    }
}

tasks.shadowJar {
    archiveBaseName.set("PandaEye")
}

// Runs against the shaded jar itself, so a minimisation or relocation mistake fails the build. The test
// compiles against the unshaded modules (PandaEye's own packages are not relocated) but runs with only the
// shaded jar and JUnit on its classpath. Compiling against the shaded jar would be circular: shadow's
// minimiser reads every source set's classes.
val smokeTest: SourceSet by sourceSets.creating
val shadedJar = tasks.shadowJar.flatMap { it.archiveFile }

dependencies {
    "smokeTestCompileOnly"(project(":common"))
    "smokeTestImplementation"(platform(libs.junit.bom))
    "smokeTestImplementation"(libs.junit.jupiter)
    "smokeTestRuntimeOnly"(libs.junit.launcher)
}

val shadedJarTest = tasks.register<Test>("shadedJarTest") {
    description = "Exercises protobuf and zstd inside the relocated, minimised plugin jar"
    group = "verification"
    testClassesDirs = smokeTest.output.classesDirs
    classpath = smokeTest.output + configurations[smokeTest.runtimeClasspathConfigurationName] + files(shadedJar)
    systemProperty("pandaeye.jar", shadedJar.get().asFile.absolutePath)
    inputs.file(shadedJar).withPropertyName("shadedJar")
    dependsOn(tasks.shadowJar)
}

tasks.check {
    dependsOn(shadedJarTest)
}
