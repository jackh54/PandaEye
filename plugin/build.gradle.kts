plugins {
    alias(libs.plugins.shadow) apply false
    alias(libs.plugins.protobuf) apply false
}

subprojects {
    plugins.withType<JavaPlugin> {
        tasks.withType<JavaCompile>().configureEach {
            options.release.set(21)
            options.encoding = "UTF-8"
            options.compilerArgs.addAll(listOf("-Xlint:all,-processing,-serial", "-parameters"))
        }
        tasks.withType<Test>().configureEach {
            useJUnitPlatform()
            maxHeapSize = "1g"
            testLogging {
                events("failed", "skipped")
                exceptionFormat = org.gradle.api.tasks.testing.logging.TestExceptionFormat.FULL
            }
        }
        dependencies {
            "testImplementation"(platform(rootProject.libs.junit.bom))
            "testImplementation"(rootProject.libs.junit.jupiter)
            "testRuntimeOnly"(rootProject.libs.junit.launcher)
        }
    }
}
