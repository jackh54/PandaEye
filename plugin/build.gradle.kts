import com.github.jengelman.gradle.plugins.shadow.tasks.ShadowJar

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

    // Both plugin jars shade protocol + common + protobuf-javalite + aircompressor, relocated and minimised.
    plugins.withId("com.gradleup.shadow") {
        tasks.named<Jar>("jar") {
            archiveClassifier.set("plain")
        }
        tasks.named<ShadowJar>("shadowJar") {
            archiveClassifier.set("")
            relocate("com.google.protobuf", "dev.pandascript.pandaeye.libs.protobuf")
            relocate("io.airlift.compress", "dev.pandascript.pandaeye.libs.aircompressor")
            // protobuf-javalite is kept whole: its schema runtime loads classes reflectively and the minimiser
            // would keep every class anyway. aircompressor is minimised down to what zstd needs.
            minimize {
                exclude(project(":protocol"))
                exclude(project(":common"))
                exclude(dependency("com.google.protobuf:protobuf-javalite:.*"))
            }
            exclude("google/protobuf/**", "pandaeye/**/*.proto", "META-INF/maven/**", "META-INF/versions/**",
                "**/module-info.class", "META-INF/LICENSE*", "META-INF/NOTICE*")
        }
        tasks.named("assemble") {
            dependsOn("shadowJar")
        }
    }
}
