plugins {
    java
    alias(libs.plugins.shadow)
}

description = "PandaEye plugin for Velocity"

dependencies {
    implementation(project(":common"))
    compileOnly(libs.velocity.api)

    testImplementation(libs.velocity.api)
    testImplementation(libs.archunit)
}

tasks.processResources {
    val version = project.version.toString()
    inputs.property("version", version)
    filesMatching("velocity-plugin.json") {
        expand("version" to version)
    }
}

tasks.shadowJar {
    archiveBaseName.set("pandaeye-velocity")
}
