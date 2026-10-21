plugins {
    java
    alias(libs.plugins.shadow)
}

description = "PandaEye plugin for Velocity"

dependencies {
    implementation(project(":common"))
    compileOnly(libs.velocity.api)
}

tasks.processResources {
    val version = project.version.toString()
    inputs.property("version", version)
    filesMatching("velocity-plugin.json") {
        expand("version" to version)
    }
}

tasks.jar {
    archiveClassifier.set("plain")
}

tasks.shadowJar {
    archiveBaseName.set("pandaeye-velocity")
    archiveClassifier.set("")
    relocate("com.google.protobuf", "dev.pandascript.pandaeye.libs.protobuf")
    relocate("io.airlift.compress", "dev.pandascript.pandaeye.libs.aircompressor")
    minimize {
        exclude(project(":protocol"))
        exclude(project(":common"))
        exclude(dependency("com.google.protobuf:protobuf-javalite:.*"))
    }
    exclude("google/protobuf/**", "META-INF/maven/**", "META-INF/versions/**", "**/module-info.class",
        "META-INF/LICENSE*", "META-INF/NOTICE*")
}

tasks.assemble {
    dependsOn(tasks.shadowJar)
}
