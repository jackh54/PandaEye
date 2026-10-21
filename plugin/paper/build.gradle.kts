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

tasks.jar {
    archiveClassifier.set("plain")
}

tasks.shadowJar {
    archiveBaseName.set("PandaEye")
    archiveClassifier.set("")
    relocate("com.google.protobuf", "dev.pandascript.pandaeye.libs.protobuf")
    relocate("io.airlift.compress", "dev.pandascript.pandaeye.libs.aircompressor")
    minimize {
        exclude(project(":protocol"))
        exclude(project(":common"))
        exclude(dependency("com.google.protobuf:protobuf-javalite:.*"))
    }
    exclude("google/protobuf/**", "META-INF/maven/**", "META-INF/versions/**", "META-INF/*.kotlin_module",
        "**/module-info.class", "META-INF/LICENSE*", "META-INF/NOTICE*")
}

tasks.assemble {
    dependsOn(tasks.shadowJar)
}
